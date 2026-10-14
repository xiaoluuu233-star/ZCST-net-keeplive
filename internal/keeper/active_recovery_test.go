package keeper

import (
	"campus-net/internal/network"
	"campus-net/internal/portal"
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// Keep the real scheduler and replace only network I/O, so cancellation,
// deadlines, command ordering and state transitions remain observable.
type controlledBackend struct {
	check func(context.Context) network.Observation
	login func(context.Context) portal.Result
	reset func()
}

func (b *controlledBackend) Check(ctx context.Context) network.Observation { return b.check(ctx) }
func (b *controlledBackend) Login(ctx context.Context) portal.Result       { return b.login(ctx) }
func (b *controlledBackend) ResetProbe() {
	if b.reset != nil {
		b.reset()
	}
}

func quietKeeper(b Backend, interval time.Duration, threshold int) *Keeper {
	c := testConfig()
	c.CheckInterval, c.FailThreshold = interval, threshold
	return New(c, b, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestTrustedCaptiveBypassesFailureThreshold(t *testing.T) {
	called := make(chan struct{}, 1)
	b := &controlledBackend{
		check: func(context.Context) network.Observation {
			return network.Observation{Captive: true, PortalReachable: true}
		},
		login: func(context.Context) portal.Result {
			called <- struct{}{}
			return portal.Result{State: portal.BadCredential}
		},
	}
	start(t, quietKeeper(b, time.Second, 50))
	select {
	case <-called:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("trusted captive waited for another full interval/failure threshold")
	}
}

func TestGenericFailuresRespectEvidenceAndThreshold(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		rechecked, reachable  bool
		threshold, wantChecks int
	}{
		{"single failures", false, true, 3, 3},
		{"fresh confirmation", true, true, 2, 1},
		{"larger custom threshold", true, true, 5, 3},
		{"link down", true, false, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var checks atomic.Int32
			called := make(chan int, 1)
			b := &controlledBackend{
				check: func(context.Context) network.Observation {
					checks.Add(1)
					return network.Observation{Rechecked: tc.rechecked, PortalReachable: tc.reachable}
				},
				login: func(context.Context) portal.Result {
					called <- int(checks.Load())
					return portal.Result{State: portal.BadCredential}
				},
			}
			k := quietKeeper(b, 30*time.Millisecond, tc.threshold)
			start(t, k)
			if tc.wantChecks == 0 {
				await(t, func() bool { return checks.Load() >= 3 })
				select {
				case <-called:
					t.Fatal("link down attempted authentication")
				default:
				}
				return
			}
			select {
			case got := <-called:
				if got != tc.wantChecks {
					t.Fatalf("login after %d checks, want %d", got, tc.wantChecks)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("authentication not triggered")
			}
		})
	}
}

func TestFastCaptiveEventsPreserveBudgetCooldownAndCredentialGuard(t *testing.T) {
	for _, state := range []portal.State{portal.Failed, portal.BadCredential} {
		t.Run(string(state), func(t *testing.T) {
			var calls, checks atomic.Int32
			var online atomic.Bool
			b := &controlledBackend{
				check: func(context.Context) network.Observation {
					checks.Add(1)
					return network.Observation{Online: online.Load(), Captive: true, PortalReachable: true}
				},
				login: func(context.Context) portal.Result { calls.Add(1); return portal.Result{State: state} },
			}
			c := testConfig()
			c.CheckInterval = time.Millisecond
			c.MaxRetries = 2
			c.Cooldown = 300 * time.Millisecond
			k := New(c, b, slog.New(slog.NewTextHandler(io.Discard, nil)))
			start(t, k)
			want := int32(2)
			if state == portal.BadCredential {
				want = 1
			}
			await(t, func() bool {
				return calls.Load() == want && (k.Snapshot().State == Cooling || k.Snapshot().State == CredentialError)
			})
			next := k.Snapshot().NextRetry
			n := checks.Load()
			await(t, func() bool { return checks.Load() >= n+20 })
			if calls.Load() != want || k.Snapshot().NextRetry != next {
				t.Fatal("automatic events reset retry budget/cooldown/credential guard")
			}
			online.Store(true)
			await(t, func() bool { return k.Snapshot().State == Online })
			if k.Snapshot().Attempts != 0 {
				t.Fatal("real online did not clear recovery state")
			}
			k.Pause(true)
			await(t, func() bool { return k.Snapshot().Paused })
			n = checks.Load()
			online.Store(false)
			time.Sleep(25 * time.Millisecond)
			if checks.Load() != n || calls.Load() != want || !k.Snapshot().Paused {
				t.Fatal("automatic activity escaped user pause")
			}
			k.Pause(false)
			await(t, func() bool { return calls.Load() > want })
		})
	}
}

func TestSlowProbeUsesFixedCadenceAndNeverOverlaps(t *testing.T) {
	var active, maximum atomic.Int32
	starts := make(chan time.Time, 5)
	b := &controlledBackend{
		check: func(ctx context.Context) network.Observation {
			n := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
			}
			starts <- time.Now()
			select {
			case <-ctx.Done():
			case <-time.After(90 * time.Millisecond):
			}
			return network.Observation{Online: true}
		},
		login: func(context.Context) portal.Result { t.Error("online probe caused login"); return portal.Result{} },
	}
	start(t, quietKeeper(b, 70*time.Millisecond, 2))
	first, second := <-starts, <-starts
	if gap := second.Sub(first); gap > 135*time.Millisecond {
		t.Fatalf("slow check added full interval: %v", gap)
	}
	if maximum.Load() != 1 {
		t.Fatalf("overlapping probes: %d", maximum.Load())
	}
}

func TestRetryAndStopAwaitCanceledProbe(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "stop"}[stop], func(t *testing.T) {
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var checks, logins atomic.Int32
			b := &controlledBackend{
				check: func(ctx context.Context) network.Observation {
					if checks.Add(1) == 1 {
						close(entered)
						<-ctx.Done()
						close(canceled)
						<-release
					}
					return network.Observation{Captive: true, PortalReachable: true}
				},
				login: func(context.Context) portal.Result { logins.Add(1); return portal.Result{State: portal.BadCredential} },
			}
			k := quietKeeper(b, time.Millisecond, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { k.Run(ctx); close(done) }()
			<-entered
			if stop {
				cancel()
			} else {
				k.Retry()
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("probe cancellation not delivered")
			}
			select {
			case <-done:
				t.Error("Run returned before probe finished")
			default:
			}
			if checks.Load() != 1 || logins.Load() != 0 {
				t.Error("new I/O overlapped canceled probe")
			}
			close(release)
			if !stop {
				await(t, func() bool { return logins.Load() == 1 })
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("worker did not exit")
			}
		})
	}
}

func TestManualCommandsCancelLoginWithoutOverlap(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var logins, active, maximum atomic.Int32
	b := &controlledBackend{
		check: func(context.Context) network.Observation {
			return network.Observation{Captive: true, PortalReachable: true}
		},
		login: func(ctx context.Context) portal.Result {
			n := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
			}
			if logins.Add(1) == 1 {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
			}
			return portal.Result{State: portal.BadCredential}
		},
	}
	k := quietKeeper(b, time.Millisecond, 1)
	start(t, k)
	<-entered
	for i := 0; i < 40; i++ {
		k.Retry()
		k.Pause(true)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("login did not observe command cancellation")
	}
	if logins.Load() != 1 {
		t.Fatal("second Login started before first returned")
	}
	close(release)
	await(t, func() bool { return k.Snapshot().Paused })
	time.Sleep(15 * time.Millisecond)
	if maximum.Load() != 1 || logins.Load() != 1 {
		t.Fatal("pause/retry commands overlapped or escaped final pause")
	}
	k.Retry()
	await(t, func() bool { return logins.Load() == 2 })
}

func TestLoginUsesFreshConfirmationAndRestartsMonitoringEachCycle(t *testing.T) {
	var logins, checks, resets atomic.Int32
	var online atomic.Bool
	var loginFinished atomic.Bool
	b := &controlledBackend{
		check: func(context.Context) network.Observation {
			checks.Add(1)
			if loginFinished.Load() && resets.Load() < 2*logins.Load() {
				t.Error("post-login confirmation reused old probe connections")
			}
			return network.Observation{Online: online.Load(), Captive: !online.Load(), PortalReachable: true}
		},
		login: func(context.Context) portal.Result {
			logins.Add(1)
			online.Store(true)
			loginFinished.Store(true)
			return portal.Result{State: portal.Success}
		},
		reset: func() { resets.Add(1) },
	}
	k := quietKeeper(b, 2*time.Millisecond, 20)
	start(t, k)
	for i := int32(1); i <= 3; i++ {
		await(t, func() bool { return logins.Load() == i && k.Snapshot().State == Online })
		n := checks.Load()
		await(t, func() bool { return checks.Load() >= n+3 })
		loginFinished.Store(false)
		if i < 3 {
			online.Store(false)
		}
	}
	if resets.Load() < 6 {
		t.Fatal("transitions did not reset idle probe connections")
	}
}

func TestPreLoginOnlineCannotConfirmFailedExternalConnectivity(t *testing.T) {
	var startedLogin, inLogin atomic.Bool
	var calls, checks, overlappingChecks atomic.Int32
	b := &controlledBackend{
		check: func(ctx context.Context) network.Observation {
			n := checks.Add(1)
			if inLogin.Load() {
				overlappingChecks.Add(1)
			}
			// An old monitoring generation can report online after the first
			// captive event, but every post-login request is actually offline.
			return network.Observation{Online: n > 1 && !startedLogin.Load(), Captive: true, PortalReachable: true}
		},
		login: func(context.Context) portal.Result {
			calls.Add(1)
			startedLogin.Store(true)
			inLogin.Store(true)
			time.Sleep(20 * time.Millisecond)
			inLogin.Store(false)
			return portal.Result{State: portal.Success}
		},
	}
	k := quietKeeper(b, time.Millisecond, 1)
	start(t, k)
	await(t, func() bool { return calls.Load() == 1 && k.Snapshot().State == Cooling })
	if k.Snapshot().State == Online || checks.Load() < 2 {
		t.Fatal("Portal success or stale observation replaced fresh confirmation")
	}
	if overlappingChecks.Load() != 0 {
		t.Fatal("pre-login monitoring was not joined before authentication")
	}
}

func TestPauseAtProbeResetPreventsStartingLogin(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	b := &controlledBackend{
		check: func(context.Context) network.Observation {
			return network.Observation{Captive: true, PortalReachable: true}
		},
		login: func(context.Context) portal.Result { calls.Add(1); return portal.Result{State: portal.BadCredential} },
		reset: func() { close(entered); <-release },
	}
	k := quietKeeper(b, time.Millisecond, 1)
	start(t, k)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reset not reached")
	}
	k.Pause(true)
	close(release)
	await(t, func() bool { return k.Snapshot().Paused })
	if calls.Load() != 0 {
		t.Fatal("Pause already canceled authentication but Login still started")
	}
}

func TestRepeatedCommandCancellationAndStopJoinAllBackendCalls(t *testing.T) {
	var total, active atomic.Int32
	for i := 0; i < 15; i++ {
		b := &controlledBackend{
			check: func(ctx context.Context) network.Observation {
				active.Add(1)
				defer active.Add(-1)
				total.Add(1)
				select {
				case <-ctx.Done():
				case <-time.After(time.Millisecond):
				}
				return network.Observation{Online: true}
			},
			login: func(context.Context) portal.Result { t.Error("online check triggered Login"); return portal.Result{} },
		}
		k := quietKeeper(b, time.Millisecond, 2)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { k.Run(ctx); close(done) }()
		await(t, func() bool { return active.Load() > 0 })
		for j := 0; j < 20; j++ {
			k.Retry()
			k.Pause(true)
			k.Pause(false)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("canceled command flood leaked worker")
		}
		if active.Load() != 0 {
			t.Fatal("Run returned while backend call remained active")
		}
	}
	n := total.Load()
	time.Sleep(15 * time.Millisecond)
	if total.Load() != n {
		t.Fatal("stopped probe task is still making requests")
	}
}
