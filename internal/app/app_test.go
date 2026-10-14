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

type testWorker struct {
	mu      *sync.Mutex
	active  *int
	overlap *bool
}

func (w *testWorker) Run(ctx context.Context) {
	w.mu.Lock()
	*w.active++
	if *w.active > 1 {
		*w.overlap = true
	}
	w.mu.Unlock()
	<-ctx.Done()
	w.mu.Lock()
	*w.active--
	w.mu.Unlock()
}
func (*testWorker) Pause(bool)                {}
func (*testWorker) Retry()                    {}
func (*testWorker) Snapshot() keeper.Snapshot { return keeper.Snapshot{State: "test"} }
func TestReloadRereadsAndKeepsValidRuntimeOnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "配置.toml")
	write := func(body string) {
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("userid='one'\npassword='fake'\n")
	var mu sync.Mutex
	active := 0
	overlap := false
	ids := []string{}
	factory := func(cfg config.Config) (Worker, io.Closer, error) {
		ids = append(ids, cfg.UserID)
		return &testWorker{&mu, &active, &overlap}, nil, nil
	}
	c := New(p, factory)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	time.Sleep(10 * time.Millisecond)
	write("userid='two'\npassword='fake'\n")
	if err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	write("userid='bad'\n")
	if err := c.Reload(); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if c.Config().UserID != "two" || len(ids) != 2 || ids[0] != "one" || ids[1] != "two" {
		t.Fatal("reload did not actually apply config")
	}
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if overlap {
		t.Fatal("workers overlap during reload")
	}
}
