package keeper

import (
	"campus-net/internal/config"
	"campus-net/internal/network"
	"campus-net/internal/portal"
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu     sync.Mutex
	logins int
	state  portal.State
	online bool
}

func (f *fakeBackend) Check(ctx context.Context) network.Observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return network.Observation{Online: f.online, Captive: true, PortalReachable: true}
}
func (f *fakeBackend) Login(ctx context.Context) portal.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logins++
	return portal.Result{State: f.state}
}
func (f *fakeBackend) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.logins }
func testConfig() config.Config {
	c := config.Default()
	c.UserID = "fake"
	c.Password = "fake"
	c.CheckInterval = 5 * time.Millisecond
	c.RetryMin = 5 * time.Millisecond
	c.RetryMax = 10 * time.Millisecond
	c.Cooldown = time.Second
	c.ConfirmTimeout = 15 * time.Millisecond
	c.FailThreshold = 1
	c.MaxRetries = 1
	return c
}
func await(t *testing.T, condition func() bool) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("state did not arrive")
}
func start(t *testing.T, k *Keeper) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { k.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("keeper ignored cancellation")
		}
	})
	return cancel
}
func TestRetryLimitEntersCooldown(t *testing.T) {
	f := &fakeBackend{state: portal.Failed}
	k := New(testConfig(), f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	start(t, k)
	await(t, func() bool { return k.Snapshot().State == Cooling })
	time.Sleep(40 * time.Millisecond)
	if f.count() != 1 {
		t.Fatalf("retry limit ignored: %d", f.count())
	}
	k.Retry()
	await(t, func() bool { return f.count() == 2 })
}
func TestPortalSuccessRequiresExternalConfirmation(t *testing.T) {
	f := &fakeBackend{state: portal.Success}
	k := New(testConfig(), f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	start(t, k)
	await(t, func() bool { return k.Snapshot().State == Cooling })
	if k.Snapshot().State == Online {
		t.Fatal("portal success marked online without probe")
	}
	f.mu.Lock()
	f.online = true
	f.mu.Unlock()
	await(t, func() bool { return k.Snapshot().State == Online })
}
func TestBadCredentialsStopAuthenticationUntilManualRetry(t *testing.T) {
	f := &fakeBackend{state: portal.BadCredential}
	k := New(testConfig(), f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	start(t, k)
	await(t, func() bool { return k.Snapshot().State == CredentialError })
	time.Sleep(35 * time.Millisecond)
	if f.count() != 1 {
		t.Fatal("credential error keeps logging in")
	}
	k.Retry()
	await(t, func() bool { return f.count() == 2 })
	k.Pause(true)
	await(t, func() bool { return k.Snapshot().Paused })
	n := f.count()
	time.Sleep(20 * time.Millisecond)
	if f.count() != n {
		t.Fatal("pause did not stop authentication")
	}
}
