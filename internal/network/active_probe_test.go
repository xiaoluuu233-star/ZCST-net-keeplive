package network

import (
	"campus-net/internal/config"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func probeConfig(t *testing.T, primary, portal string) (config.Config, *http.Client) {
	t.Helper()
	cfg := config.Default()
	cfg.ConnectivityURL, cfg.ConnectivityBody = primary, "network-ok"
	cfg.PortalBaseURL = portal
	cfg.ProbeTimeout = 200 * time.Millisecond
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return cfg, c
}

func TestTrustedRedirectDoesNotRequestPortal(t *testing.T) {
	var portalCalls atomic.Int32
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		portalCalls.Add(1)
		<-r.Context().Done()
	}))
	defer portal.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, portal.URL+"/portal.do", http.StatusFound)
	}))
	defer primary.Close()
	cfg, c := probeConfig(t, primary.URL, portal.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	o := Check(ctx, c, cfg)
	if !o.Captive || !o.PortalReachable || o.Online || o.Rechecked {
		t.Fatalf("wrong evidence: %+v", o)
	}
	if portalCalls.Load() != 0 || time.Since(start) > 150*time.Millisecond {
		t.Fatal("trusted redirect waited for an unnecessary Portal request")
	}
}

func TestTransientPrimaryFailureUsesFreshConnection(t *testing.T) {
	for _, failure := range []string{"http500", "body-eof", "unexpected-body"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			var mu sync.Mutex
			var addresses []string
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				addresses = append(addresses, r.RemoteAddr)
				mu.Unlock()
				if calls.Add(1) == 1 {
					switch failure {
					case "http500":
						w.WriteHeader(http.StatusInternalServerError)
					case "body-eof":
						conn, _, _ := w.(http.Hijacker).Hijack()
						conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 40\r\n\r\nbroken"))
						conn.Close()
					default:
						w.Write([]byte("bad-body"))
					}
					return
				}
				w.Write([]byte("network-ok"))
			}))
			defer primary.Close()
			portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
			defer portal.Close()
			cfg, c := probeConfig(t, primary.URL, portal.URL)
			o := Check(context.Background(), c, cfg)
			if !o.Online || o.Captive || o.Rechecked {
				t.Fatalf("transient failure became offline: %+v", o)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(addresses) != 2 || addresses[0] == addresses[1] {
				t.Fatalf("not a fresh primary connection: %v", addresses)
			}
		})
	}
}

func TestNormalProbeReusesConnectionAndHandlesServerClose(t *testing.T) {
	for _, closeConnection := range []bool{false, true} {
		t.Run(map[bool]string{false: "reuse", true: "connection-close"}[closeConnection], func(t *testing.T) {
			var connections atomic.Int32
			primary := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if closeConnection {
					w.Header().Set("Connection", "close")
				}
				w.Write([]byte("network-ok"))
			}))
			primary.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					connections.Add(1)
				}
			}
			primary.Start()
			defer primary.Close()
			cfg, c := probeConfig(t, primary.URL, primary.URL)
			for i := 0; i < 3; i++ {
				if o := Check(context.Background(), c, cfg); !o.Online || o.Captive {
					t.Fatalf("ordinary close/reuse is offline: %+v", o)
				}
			}
			if !closeConnection && connections.Load() != 1 {
				t.Fatalf("normal probing lost pooling: %d connections", connections.Load())
			}
		})
	}
}

func TestAmbiguousFailureRechecksConcurrentlyWithinBudget(t *testing.T) {
	var primaryCalls atomic.Int32
	started := make(chan string, 8)
	slow := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { started <- name; <-r.Context().Done() }
	}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primaryCalls.Add(1) == 1 {
			w.WriteHeader(500)
			return
		}
		slow("fresh-primary")(w, r)
	}))
	defer primary.Close()
	backup := httptest.NewServer(slow("backup"))
	defer backup.Close()
	portal := httptest.NewServer(slow("portal"))
	defer portal.Close()
	cfg, c := probeConfig(t, primary.URL, portal.URL)
	cfg.BackupTargets = []config.Target{{URL: backup.URL, StatusCode: 200}, {URL: backup.URL, StatusCode: 200}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	o := Check(ctx, c, cfg)
	elapsed := time.Since(start)
	if elapsed > 350*time.Millisecond {
		t.Fatalf("recheck exceeded shared budget: %s (%+v)", elapsed, o)
	}
	seen := map[string]bool{}
	for len(started) > 0 {
		seen[<-started] = true
	}
	if !seen["fresh-primary"] || !seen["backup"] || !seen["portal"] {
		t.Fatalf("not all rechecks ran concurrently: %v", seen)
	}
	if o.Online || o.Captive || !o.Rechecked {
		t.Fatalf("blackhole trusted as online/captive: %+v", o)
	}
}

func TestProbeCancellationCancelsEveryInFlightRequest(t *testing.T) {
	started := make(chan struct{}, 4)
	finished := make(chan struct{}, 4)
	var primaryCalls atomic.Int32
	slow := func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		finished <- struct{}{}
	}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primaryCalls.Add(1) == 1 {
			w.WriteHeader(500)
			return
		}
		slow(w, r)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(slow))
	defer backup.Close()
	portal := httptest.NewServer(http.HandlerFunc(slow))
	defer portal.Close()
	cfg, c := probeConfig(t, primary.URL, portal.URL)
	cfg.ProbeTimeout = time.Second
	cfg.BackupTargets = []config.Target{{URL: backup.URL, StatusCode: 200}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Observation, 1)
	go func() { done <- Check(ctx, c, cfg) }()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(700 * time.Millisecond):
			cancel()
			<-done
			t.Fatal("parallel requests did not all start")
		}
	}
	cancel()
	select {
	case o := <-done:
		if o.Rechecked {
			t.Fatalf("parent cancellation counted as fresh failure: %+v", o)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Check did not join canceled requests")
	}
	for i := 0; i < 3; i++ {
		select {
		case <-finished:
		case <-time.After(300 * time.Millisecond):
			t.Fatal("orphaned HTTP request after cancellation")
		}
	}
}

func TestUnknownRedirectNeverBecomesCaptive(t *testing.T) {
	var unknownCalls atomic.Int32
	unknown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { unknownCalls.Add(1); w.WriteHeader(200) }))
	defer unknown.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, unknown.URL+"/login", 302) }))
	defer primary.Close()
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer portal.Close()
	cfg, c := probeConfig(t, primary.URL, portal.URL)
	o := Check(context.Background(), c, cfg)
	if o.Captive || o.Online || !o.PortalReachable || !o.Rechecked {
		t.Fatalf("unknown redirect trusted: %+v", o)
	}
	if unknownCalls.Load() != 0 {
		t.Fatal("probe followed login redirect")
	}
}

func TestPrimaryTimeoutAndRecheckUseTwoBudgets(t *testing.T) {
	var calls atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer slow.Close()
	cfg, c := probeConfig(t, slow.URL, slow.URL)
	cfg.BackupTargets = []config.Target{{URL: slow.URL, StatusCode: 200}, {URL: slow.URL, StatusCode: 200}, {URL: slow.URL, StatusCode: 200}}
	start := time.Now()
	o := Check(context.Background(), c, cfg)
	elapsed := time.Since(start)
	if elapsed < 350*time.Millisecond || elapsed > 600*time.Millisecond {
		t.Fatalf("primary plus shared recheck must use two 200ms budgets: %s", elapsed)
	}
	if calls.Load() != 6 || !o.Rechecked || o.Captive || o.PortalReachable || o.Online {
		t.Fatalf("wrong timeout evidence (%d requests): %+v", calls.Load(), o)
	}
}

func TestHealthyBackupWinsWithoutWaitingForSlowBackup(t *testing.T) {
	var portalCalls atomic.Int32
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { portalCalls.Add(1); w.WriteHeader(200) }))
	defer portal.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, portal.URL+"/login", 302) }))
	defer primary.Close()
	slowStarted, slowFinished := make(chan struct{}), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(slowStarted)
		<-r.Context().Done()
		close(slowFinished)
	}))
	defer slow.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-slowStarted; w.Write([]byte("network-ok")) }))
	defer healthy.Close()
	cfg, c := probeConfig(t, primary.URL, portal.URL)
	cfg.BackupTargets = []config.Target{{URL: slow.URL, StatusCode: 200}, {URL: healthy.URL, StatusCode: 200, Body: "network-ok"}}
	start := time.Now()
	o := Check(context.Background(), c, cfg)
	if !o.Online || o.Captive || o.Rechecked || time.Since(start) > 150*time.Millisecond {
		t.Fatalf("healthy parallel backup lost: %+v", o)
	}
	if portalCalls.Load() != 0 {
		t.Fatal("strong redirect unnecessarily probed Portal")
	}
	select {
	case <-slowFinished:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("early online left slow backup running")
	}
}
