package network

import (
	"campus-net/internal/config"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDirectIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	cfg := config.Default()
	cfg.UserID = "test"
	cfg.Password = "secret"
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr := c.Transport.(*http.Transport)
	if tr.Proxy != nil {
		req, _ := http.NewRequest("GET", "http://example.test/", nil)
		u, _ := tr.Proxy(req)
		if u != nil {
			t.Fatal("direct client reads environment proxy")
		}
	}
}
func TestProbeDistinguishesCaptiveAndBackup(t *testing.T) {
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer portal.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, portal.URL+"/portal.do?wlanuserip=10.0.0.2", 302)
	}))
	defer primary.Close()
	cfg := config.Default()
	cfg.PortalBaseURL = portal.URL
	cfg.ConnectivityURL = primary.URL
	c, _ := New(cfg)
	obs := Check(context.Background(), c, cfg)
	if obs.Online || !obs.Captive || !obs.PortalReachable {
		t.Fatalf("wrong observation %+v", obs)
	}
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("network-ok")) }))
	defer backup.Close()
	cfg.BackupTargets = []config.Target{{URL: backup.URL, Body: "network-ok", StatusCode: 200}}
	obs = Check(context.Background(), c, cfg)
	if !obs.Online {
		t.Fatalf("healthy backup ignored: %+v", obs)
	}
}
