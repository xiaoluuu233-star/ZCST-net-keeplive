package app

import (
	"campus-net/internal/config"
	"campus-net/internal/keeper"
	"campus-net/internal/logging"
	"campus-net/internal/network"
	"campus-net/internal/portal"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

type Worker interface {
	Run(context.Context)
	Pause(bool)
	Retry()
	Snapshot() keeper.Snapshot
}
type Factory func(config.Config) (Worker, io.Closer, error)
type Controller struct {
	mu      sync.RWMutex
	viewMu  sync.RWMutex
	path    string
	factory Factory
	cfg     config.Config
	worker  Worker
	cleanup io.Closer
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	closed  bool
}

func New(path string, f Factory) *Controller {
	if f == nil {
		f = DefaultFactory
	}
	return &Controller{path: path, factory: f}
}
func (c *Controller) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx != nil || c.closed {
		return errors.New("运行控制器已经启动或关闭")
	}
	c.ctx = ctx
	return c.replace()
}
func (c *Controller) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx == nil || c.closed {
		return errors.New("程序尚未运行")
	}
	return c.replace()
}

// 在验证/创建新配置成功后，先停止并等待旧任务，然后才开始新任务。
// 因此无效重载不会破坏现有运行，也不会出现两次认证并发。
func (c *Controller) replace() error {
	cfg, err := config.Load(c.path)
	if err != nil {
		return err
	}
	worker, cleanup, err := c.factory(cfg)
	if err != nil {
		return err
	}
	paused := c.worker != nil && c.worker.Snapshot().Paused
	c.stopLocked()
	c.viewMu.Lock()
	c.cfg, c.worker, c.cleanup = cfg, worker, cleanup
	c.viewMu.Unlock()
	ctx, cancel := context.WithCancel(c.ctx)
	c.cancel = cancel
	c.done = make(chan struct{})
	if paused {
		worker.Pause(true)
	}
	done := c.done
	go func() { defer close(done); worker.Run(ctx) }()
	return nil
}
func (c *Controller) stopLocked() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
		c.cancel = nil
	}
	if c.cleanup != nil {
		c.cleanup.Close()
		c.cleanup = nil
	}
}
func (c *Controller) Stop()                 { c.mu.Lock(); defer c.mu.Unlock(); c.closed = true; c.stopLocked() }
func (c *Controller) Config() config.Config { c.viewMu.RLock(); defer c.viewMu.RUnlock(); return c.cfg }
func (c *Controller) Snapshot() keeper.Snapshot {
	c.viewMu.RLock()
	w := c.worker
	c.viewMu.RUnlock()
	if w == nil {
		return keeper.Snapshot{State: "未启动"}
	}
	return w.Snapshot()
}
func (c *Controller) Pause(p bool) {
	c.viewMu.RLock()
	defer c.viewMu.RUnlock()
	if c.worker != nil {
		c.worker.Pause(p)
	}
}
func (c *Controller) Retry() {
	c.viewMu.RLock()
	defer c.viewMu.RUnlock()
	if c.worker != nil {
		c.worker.Retry()
	}
}

type backend struct {
	cfg       config.Config
	http      *http.Client
	portal    *portal.Client
	probeMu   sync.Mutex
	lastFresh time.Time
}

func (b *backend) Check(ctx context.Context) network.Observation {
	// 定期将一次普通心跳换成新连接检查，防止旧 TCP 流持续放行
	// 掩盖认证已失效；不增加请求次数，也不移除 Portal 会话 Cookie。
	b.probeMu.Lock()
	if time.Since(b.lastFresh) >= 5*time.Second {
		b.http.CloseIdleConnections()
		b.lastFresh = time.Now()
	}
	b.probeMu.Unlock()
	return network.Check(ctx, b.http, b.cfg)
}
func (b *backend) Login(ctx context.Context) portal.Result { return b.portal.Login(ctx) }

// 认证前后丢弃空闲探测连接；Portal 的 CookieJar 不受影响。
func (b *backend) ResetProbe() {
	b.probeMu.Lock()
	defer b.probeMu.Unlock()
	b.http.CloseIdleConnections()
	b.lastFresh = time.Now()
}

type resources struct {
	http *http.Client
	log  io.Closer
}

func (r resources) Close() error { r.http.CloseIdleConnections(); return r.log.Close() }
func DefaultFactory(cfg config.Config) (Worker, io.Closer, error) {
	h, err := network.New(cfg)
	if err != nil {
		return nil, nil, err
	}
	log, closer, err := logging.New(cfg)
	if err != nil {
		h.CloseIdleConnections()
		return nil, nil, err
	}
	b := &backend{cfg: cfg, http: h, portal: portal.New(cfg, h)}
	return keeper.New(cfg, b, log), resources{h, closer}, nil
}
