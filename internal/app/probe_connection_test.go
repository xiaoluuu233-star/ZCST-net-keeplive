package app

import (
	"campus-net/internal/config"
	"campus-net/internal/network"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 已建立的 TCP 流可能继续放行，而新连接才会显示 Portal；不能永远
// 用旧流的成功结果来证明当前会话仍有效。
func TestBackendPeriodicallyReplacesProbeConnection(t *testing.T) {
	addresses := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addresses <- r.RemoteAddr
		w.Write([]byte("Microsoft Connect Test"))
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.ConnectivityURL = server.URL
	h, err := network.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.CloseIdleConnections()
	b := &backend{cfg: cfg, http: h}
	for i := 0; i < 2; i++ {
		if !b.Check(context.Background()).Online {
			t.Fatal("fixture offline")
		}
	}
	first, second := <-addresses, <-addresses
	if first != second {
		t.Fatal("normal heartbeats did not reuse a connection")
	}
	b.probeMu.Lock()
	b.lastFresh = time.Now().Add(-time.Minute)
	b.probeMu.Unlock()
	if !b.Check(context.Background()).Online {
		t.Fatal("fresh probe failed")
	}
	if <-addresses == first {
		t.Fatal("expired connection was reused indefinitely")
	}
}
