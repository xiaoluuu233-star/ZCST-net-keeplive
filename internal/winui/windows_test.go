//go:build windows

package winui

import (
	"context"
	"path/filepath"

	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWin64Layouts(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("amd64 layout")
	}
	if got := unsafe.Sizeof(notifyIconData{}); got != 976 {
		t.Fatalf("NOTIFYICONDATAW size = %d", got)
	}
	if got := unsafe.Offsetof(notifyIconData{}.HBalloonIcon); got != 968 {
		t.Fatalf("balloon offset = %d", got)
	}
	if got := unsafe.Sizeof(msgStruct{}); got != 48 {
		t.Fatalf("MSG size = %d", got)
	}
	if got := unsafe.Sizeof(wndClassEx{}); got != 80 {
		t.Fatalf("WNDCLASSEXW size = %d", got)
	}
}

func TestStartupCommandQuotesAbsoluteChinesePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "中文目录", "my config.toml")
	got, err := startupCommand(`C:\程序目录\campus net.exe`, path)
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\程序目录\campus net.exe" tray --config "` + path + `"`
	if got != want {
		t.Fatalf("startup command %q; want %q", got, want)
	}
}

func TestSingletonSameAbsoluteConfig(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	release, err := Acquire(config)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Acquire(strings.ToUpper(filepath.Dir(config)) + `\sub\..\config.toml`)
	if err == nil {
		t.Fatal("second instance accepted same config")
	}
	if !strings.Contains(err.Error(), "运行") {
		t.Fatalf("unhelpful error: %v", err)
	}
	otherRelease, err := Acquire(filepath.Join(filepath.Dir(config), "other.toml"))
	if err != nil {
		t.Fatal(err)
	}
	otherRelease()
	release()
	release()
	again, err := Acquire(config)
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestMenusPauseResumeAndSerializeSlowReload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	unblock := make(chan struct{})
	retried := make(chan struct{})
	paused := make(chan bool, 2)
	results := make(chan error, 8)
	opts := Options{Pause: func(v bool) { paused <- v }, Reload: func() error { close(started); <-unblock; return nil }, Retry: func() { close(retried) }}
	dispatch := newDispatcher(ctx, opts, func(err error) { results <- err })
	if !dispatch.enqueue(cmdPause, Status{}) {
		t.Fatal("enqueue pause")
	}
	if v := <-paused; !v {
		t.Fatal("pause command did not pause")
	}
	dispatch.enqueue(cmdPause, Status{Paused: true})
	if v := <-paused; v {
		t.Fatal("resume command did not resume")
	}
	dispatch.enqueue(cmdReload, Status{})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reload not started")
	}
	// 即使重载尚未结束，GUI 调用 enqueue 也必须立即返回。
	queued := make(chan bool, 1)
	go func() { queued <- dispatch.enqueue(cmdRetry, Status{}) }()
	select {
	case ok := <-queued:
		if !ok {
			t.Fatal("retry not queued")
		}
	case <-time.After(time.Second):
		t.Fatal("GUI blocked by reload")
	}
	select {
	case <-retried:
		t.Fatal("callbacks overlapped")
	case <-time.After(30 * time.Millisecond):
	}
	close(unblock)
	select {
	case <-retried:
	case <-time.After(time.Second):
		t.Fatal("queued retry not run")
	}
}

func TestNativeTraySmokeLifecycle(t *testing.T) {
	if !interactiveDesktop() {
		t.Skip("no interactive Windows desktop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var snapshots atomic.Int32
	err := Run(ctx, Options{ConfigPath: filepath.Join(t.TempDir(), "smoke.toml"), Snapshot: func() Status { snapshots.Add(1); return Status{State: "online", Detail: "离线烟雾测试"} }})
	if err != nil {
		t.Fatal(err)
	}
	if snapshots.Load() == 0 {
		t.Fatal("no status updates")
	}
}

func TestNativeMessagesContinueDuringSlowReload(t *testing.T) {
	if !interactiveDesktop() {
		t.Skip("no interactive Windows desktop")
	}
	config := filepath.Join(t.TempDir(), "smoke.toml")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan windows.Handle, 1)
	started := make(chan struct{})
	unblock := make(chan struct{})
	defer close(unblock)
	var snapshots atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{ConfigPath: config, Snapshot: func() Status {
			snapshots.Add(1)
			nativeWindows.RLock()
			for h, w := range nativeWindows.items {
				if w.options.ConfigPath == config {
					select {
					case ready <- h:
					default:
					}
				}
			}
			nativeWindows.RUnlock()
			return Status{State: "online"}
		}, Reload: func() error { close(started); <-unblock; return nil }})
	}()
	var handle windows.Handle
	select {
	case handle = <-ready:
	case err := <-done:
		t.Fatalf("tray exited: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("tray never ready")
	}
	visible, _, _ := user32.NewProc("IsWindowVisible").Call(uintptr(handle))
	parent, _, _ := user32.NewProc("GetParent").Call(uintptr(handle))
	if visible != 0 || parent != 0 {
		t.Fatalf("message window must be hidden and top level; visible=%d parent=%d", visible, parent)
	}
	postMessage.Call(uintptr(handle), wmCommand, cmdReload, 0)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reload not dispatched")
	}
	before := snapshots.Load()
	nativeWindows.RLock()
	broadcast := nativeWindows.items[handle].taskbarCreated
	nativeWindows.RUnlock()
	if broadcast == 0 {
		t.Fatal("TaskbarCreated message not registered")
	}
	post(handle, broadcast)
	post(handle, wmTimer)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for snapshots.Load() < before+2 {
		select {
		case <-deadline.C:
			t.Fatal("GUI messages blocked by reload")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing tray waited for blocked callback")
	}
	enabled, err := StartupEnabled(config)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("smoke test unexpectedly enabled startup")
	}
}

func TestCleanupBeforeMessagePumpDoesNotLeaveQuit(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := registerNativeClass(); err != nil {
		t.Fatal(err)
	}
	class, title := wide(windowClass), wide("partial initialization smoke")
	module, _, _ := getModuleHandle.Call(0)
	h, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0, 0, 0, 0, 0, 0, 0, module, 0)
	runtime.KeepAlive(class)
	runtime.KeepAlive(title)
	if h == 0 {
		t.Fatal(err)
	}
	w := &trayWindow{handle: windows.Handle(h)}
	nativeWindows.Lock()
	nativeWindows.items[w.handle] = w
	nativeWindows.Unlock()
	defer func() { nativeWindows.Lock(); delete(nativeWindows.items, w.handle); nativeWindows.Unlock() }()
	destroyWindow.Call(h)
	var message msgStruct
	found, _, _ := user32.NewProc("PeekMessageW").Call(uintptr(unsafe.Pointer(&message)), 0, 0x12, 0x12, 1)
	if found != 0 {
		t.Fatal("partial startup cleanup left WM_QUIT on a reusable OS thread")
	}
}

func TestActualKeeperStatusLabelsAndIcons(t *testing.T) {
	for _, tc := range []struct {
		state, want string
		icon        uintptr
	}{
		{"在线", "在线", 32516},
		{"重试冷却", "冷却等待", 32515},
		{"账号配置错误", "账号认证失败", 32513},
		{"已暂停", "已暂停", 32512},
		{"认证中", "认证中", 32515},
		{"待认证", "等待联网", 32515},
		{"网络故障", "网络故障", 32515},
	} {
		t.Run(tc.state, func(t *testing.T) {
			s := Status{State: tc.state}
			if got := statusLabel(s); got != tc.want {
				t.Fatalf("label=%q want %q", got, tc.want)
			}
			if got := statusIcon(s); got != tc.icon {
				t.Fatalf("icon=%d want %d", got, tc.icon)
			}
		})
	}
}

func TestConsoleHidingNeverAffectsSharedOrUnknownConsole(t *testing.T) {
	for _, tc := range []struct {
		count, handle uintptr
		want          bool
	}{
		{1, 123, true}, {2, 123, false}, {0, 123, false}, {1, 0, false},
	} {
		if got := shouldHideConsole(tc.count, tc.handle); got != tc.want {
			t.Fatalf("count=%d handle=%d hide=%v want %v", tc.count, tc.handle, got, tc.want)
		}
	}
}

func TestLogMenuReadsCurrentPathAfterReload(t *testing.T) {
	path := "first.log"
	opts := Options{LogPath: "startup.log", CurrentLogPath: func() string { return path }}
	if got := currentLogPath(opts); got != "first.log" {
		t.Fatalf("current path = %q", got)
	}
	path = "reloaded.log"
	if got := currentLogPath(opts); got != "reloaded.log" {
		t.Fatalf("stale path after reload = %q", got)
	}
	if got := currentLogPath(Options{LogPath: "fallback.log"}); got != "fallback.log" {
		t.Fatalf("fallback = %q", got)
	}
}
