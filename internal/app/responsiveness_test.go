package app

import (
	"campus-net/internal/config"
	"campus-net/internal/keeper"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type slowStopWorker struct{ started, stopping, release chan struct{} }

func (w *slowStopWorker) Run(ctx context.Context) {
	close(w.started)
	<-ctx.Done()
	close(w.stopping)
	<-w.release
}
func (*slowStopWorker) Snapshot() keeper.Snapshot { return keeper.Snapshot{State: "在线"} }
func (*slowStopWorker) Pause(bool)                {}
func (*slowStopWorker) Retry()                    {}
func TestSnapshotDoesNotBlockWhileReloadWaitsForOldWorker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("userid='test'\npassword='fake'"), 0600)
	w := &slowStopWorker{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	calls := 0
	c := New(p, func(config.Config) (Worker, io.Closer, error) {
		calls++
		if calls == 1 {
			return w, nil, nil
		}
		return &testWorker{mu: &sync.Mutex{}, active: new(int), overlap: new(bool)}, nil, nil
	})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-w.started
	done := make(chan error, 1)
	go func() { done <- c.Reload() }()
	<-w.stopping
	snap := make(chan keeper.Snapshot, 1)
	go func() { snap <- c.Snapshot() }()
	blocked := false
	select {
	case <-snap:
	case <-time.After(50 * time.Millisecond):
		blocked = true
	}
	close(w.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c.Stop()
	if blocked {
		t.Fatal("tray Snapshot blocked on config reload")
	}
}
