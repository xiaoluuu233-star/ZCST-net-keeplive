package keeper

import (
	"campus-net/internal/config"
	"campus-net/internal/network"
	"campus-net/internal/portal"
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	Online          = "在线"
	Cooling         = "重试冷却"
	CredentialError = "账号配置错误"
	PausedState     = "已暂停"
	Authenticating  = "认证中"
	Offline         = "待认证"
	LinkDown        = "网络故障"
)

type Snapshot struct {
	State, Detail string
	Paused        bool
	Attempts      int
	NextRetry     time.Time
	Updated       time.Time
}
type Backend interface {
	Check(context.Context) network.Observation
	Login(context.Context) portal.Result
}
type command struct {
	kind  string
	pause bool
}
type Keeper struct {
	cfg     config.Config
	backend Backend
	log     *slog.Logger
	mu      sync.RWMutex

	snapshot Snapshot
	commands chan command
	cancel   context.CancelFunc
}

func New(cfg config.Config, backend Backend, log *slog.Logger) *Keeper {
	if log == nil {
		log = slog.Default()
	}
	return &Keeper{cfg: cfg, backend: backend, log: log, commands: make(chan command, 16), snapshot: Snapshot{State: Offline, Updated: time.Now()}}
}
func (k *Keeper) Snapshot() Snapshot { k.mu.RLock(); defer k.mu.RUnlock(); return k.snapshot }
func (k *Keeper) publish(state, detail string, paused bool, attempts int, next time.Time) {
	k.mu.Lock()
	changed := k.snapshot.State != state || k.snapshot.Detail != detail
	k.snapshot = Snapshot{State: state, Detail: detail, Paused: paused, Attempts: attempts, NextRetry: next, Updated: time.Now()}
	k.mu.Unlock()
	if changed {
		k.log.Info("网络状态变化", "state", state, "detail", detail, "attempts", attempts)
	}
}
func (k *Keeper) send(cmd command) {
	k.mu.Lock()
	defer k.mu.Unlock()
	select {
	case k.commands <- cmd:
	default:
		// Commands express the user's latest intent. Keep the newest command
		// even when a slow canceled backend has filled the bounded queue.
		select {
		case <-k.commands:
		default:
		}
		k.commands <- cmd
	}
	if k.cancel != nil {
		k.cancel()
	}
}
func (k *Keeper) Pause(p bool) { k.send(command{kind: "pause", pause: p}) }
func (k *Keeper) Retry()       { k.send(command{kind: "retry"}) }

// beginOperation and send share the lock: a command either cancels this
// operation or stays queued and prevents it from starting. No cancellation
// can fall into the gap between replacing the probe and starting Login.
func (k *Keeper) beginOperation(ctx context.Context) (context.Context, context.CancelFunc) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if ctx.Err() != nil || len(k.commands) != 0 {
		return nil, nil
	}
	opCtx, cancel := context.WithCancel(ctx)
	k.cancel = cancel
	return opCtx, cancel
}
func (k *Keeper) endOperation(cancel context.CancelFunc) {
	cancel()
	k.mu.Lock()
	k.cancel = nil
	k.mu.Unlock()
}

type probeTask struct {
	events chan probeEvent
	done   chan struct{}
	cancel context.CancelFunc
}
type probeEvent struct {
	observation network.Observation
	started     time.Time
}

// Each task has its own event channel, so stopping it also discards every
// queued observation from that generation. Only this goroutine calls Check
// during monitoring; ticks missed by a slow request coalesce naturally.
func (k *Keeper) startProbe(ctx context.Context) *probeTask {
	probeCtx, cancel := k.beginOperation(ctx)
	if cancel == nil {
		return nil
	}
	p := &probeTask{events: make(chan probeEvent, 1), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(k.cfg.CheckInterval)
		defer ticker.Stop()
		for probeCtx.Err() == nil {
			started := time.Now()
			obs := k.backend.Check(probeCtx)
			if probeCtx.Err() != nil {
				return
			}
			event := probeEvent{observation: obs, started: started}
			select {
			case p.events <- event:
			default:
				// Retain the freshest read-only observation without allowing
				// a backlog to replay an obsolete network state.
				select {
				case <-p.events:
				default:
				}
				select {
				case p.events <- event:
				case <-probeCtx.Done():
					return
				}
			}
			select {
			case <-probeCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return p
}
func (k *Keeper) stopProbe(p *probeTask) {
	if p != nil {
		p.cancel()
		<-p.done
		k.endOperation(p.cancel)
	}
}
func (k *Keeper) resetProbe() {
	if resetter, ok := k.backend.(interface{ ResetProbe() }); ok {
		resetter.ResetProbe()
	}
}

// Run owns recovery state and serializes every Login. Monitoring runs in a
// separately cancelable task and is always joined before authentication,
// manual pause/retry and shutdown. Backend methods must honor cancellation.
func (k *Keeper) Run(ctx context.Context) {
	paused, badCredentials := false, false
	attempts, fails := 0, 0
	var nextRetry, firstAnomaly time.Time
	probe := k.startProbe(ctx)
	defer func() { k.stopProbe(probe) }()

	handleCommand := func(cmd command) {
		k.stopProbe(probe)
		probe = nil
		if cmd.kind == "pause" {
			paused = cmd.pause
		} else {
			paused, badCredentials = false, false
		}
		attempts, fails = 0, 0
		nextRetry, firstAnomaly = time.Time{}, time.Time{}
		if paused {
			k.publish(PausedState, "恢复后继续检测", true, 0, nextRetry)
		} else {
			probe = k.startProbe(ctx)
		}
	}
	for ctx.Err() == nil {
		// Drain user commands before observing automatic results. Automatic
		// observations never call Retry or clear a user pause/guard/budget.
		select {
		case cmd := <-k.commands:
			handleCommand(cmd)
			continue
		default:
		}
		if !paused && probe == nil {
			probe = k.startProbe(ctx)
		}
		var events <-chan probeEvent
		if probe != nil {
			events = probe.events
		}
		select {
		case <-ctx.Done():
			return
		case cmd := <-k.commands:
			handleCommand(cmd)
		case event := <-events:
			obs := event.observation
			if obs.Online {
				attempts, fails = 0, 0
				badCredentials = false
				nextRetry, firstAnomaly = time.Time{}, time.Time{}
				k.publish(Online, "外网连通性已确认", false, 0, nextRetry)
				continue
			}
			if firstAnomaly.IsZero() {
				firstAnomaly = event.started
			}
			if badCredentials {
				k.publish(CredentialError, "请修正配置后重载或手动重试", false, attempts, nextRetry)
				continue
			}
			if !obs.PortalReachable && !obs.Captive {
				fails = 0
				k.publish(LinkDown, "等待网络链路恢复", false, attempts, nextRetry)
				continue
			}
			fails++
			if obs.Rechecked {
				fails++
			}
			if time.Now().Before(nextRetry) {
				k.publish(Cooling, "等待下一次认证；仍在检测外网", false, attempts, nextRetry)
				continue
			}
			if !obs.Captive && fails < k.cfg.FailThreshold {
				k.publish(Offline, "等待连续失败确认", false, attempts, nextRetry)
				continue
			}
			k.stopProbe(probe)
			probe = nil
			opCtx, cancel := k.beginOperation(ctx)
			if cancel == nil {
				continue
			}
			k.resetProbe()
			if opCtx.Err() != nil {
				k.endOperation(cancel)
				continue
			}
			if attempts >= k.cfg.MaxRetries {
				attempts = 0
			}
			attempts++
			k.log.Info("网络异常已确认", "detection_elapsed", time.Since(firstAnomaly))
			k.publish(Authenticating, "正在认证，随后确认外网", false, attempts, time.Time{})
			started := time.Now()
			result := k.backend.Login(opCtx)
			k.log.Info("Portal 认证结果", "state", result.State, "code", result.Code, "http", result.HTTPStatus, "authentication_elapsed", time.Since(started))
			if opCtx.Err() != nil {
				k.endOperation(cancel)
				continue
			}
			if result.State == portal.BadCredential {
				badCredentials = true
				k.publish(CredentialError, "请修正配置后重载或手动重试", false, attempts, time.Time{})
				k.endOperation(cancel)
				continue
			}
			confirmed := false
			if result.OK() {
				k.resetProbe()
				started = time.Now()
				confirmed = k.confirm(opCtx)
				k.log.Info("认证后外网确认", "online", confirmed, "confirmation_elapsed", time.Since(started), "recovery_elapsed", time.Since(firstAnomaly))
			}
			if opCtx.Err() != nil {
				k.endOperation(cancel)
				continue
			}
			if confirmed {
				attempts, fails = 0, 0
				nextRetry, firstAnomaly = time.Time{}, time.Time{}
				k.publish(Online, "认证后外网连通性已确认", false, 0, nextRetry)
			} else {
				nextRetry = time.Now().Add(k.backoff(attempts))
				k.publish(Cooling, "认证或外网确认失败，稍后重试", false, attempts, nextRetry)
			}
			k.endOperation(cancel)
		}
	}
}
func (k *Keeper) backoff(attempts int) time.Duration {
	if attempts >= k.cfg.MaxRetries {
		return k.cfg.Cooldown
	}
	backoff := k.cfg.RetryMin
	for n := 1; n < attempts; n++ {
		if backoff >= k.cfg.RetryMax/2 {
			return k.cfg.RetryMax
		}
		backoff *= 2
	}
	return min(backoff, k.cfg.RetryMax)
}
func (k *Keeper) confirm(ctx context.Context) bool {
	confirmCtx, cancel := context.WithTimeout(ctx, k.cfg.ConfirmTimeout)
	defer cancel()
	for confirmCtx.Err() == nil {
		obs := k.backend.Check(confirmCtx)
		if confirmCtx.Err() != nil {
			return false
		}
		if obs.Online {
			return true
		}
		timer := time.NewTimer(min(100*time.Millisecond, k.cfg.ConfirmTimeout/4))
		select {
		case <-confirmCtx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return false
}
