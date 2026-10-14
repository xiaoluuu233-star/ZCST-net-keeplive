//go:build windows

package winui

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const windowClass = "CampusNet.NativeTray.v1"

func canonicalConfig(path string) (string, error) {
	if path == "" || strings.ContainsAny(path, "\x00\r\n\"") {
		return "", errors.New("配置路径为空或含非法字符")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, e := filepath.EvalSymlinks(absolute); e == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}

func configIdentity(configPath string) (string, error) {
	path, err := canonicalConfig(configPath)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(strings.ToLower(path)))
	return fmt.Sprintf("CampusNet_%x", hash[:16]), nil
}

// CLI 与托盘共用命名内核对象；保持句柄即可保持单实例，无需线程亲和的所有权。
func Acquire(configPath string) (func(), error) {
	name, err := configIdentity(configPath)
	if err != nil {
		return nil, err
	}
	ptr, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateMutex(nil, false, ptr)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		return nil, errors.New("此配置的校园网程序已在运行，请先退出已有实例")
	}
	if err != nil {
		return nil, fmt.Errorf("无法创建单实例锁: %w", err)
	}
	var once sync.Once
	return func() { once.Do(func() { windows.CloseHandle(handle) }) }, nil
}

func startupCommand(executable, configPath string) (string, error) {
	if strings.ContainsAny(executable, "\x00\r\n\"") {
		return "", errors.New("可执行文件路径含非法字符")
	}
	exe, err := filepath.Abs(executable)
	if err != nil {
		return "", err
	}
	config, err := canonicalConfig(configPath)
	if err != nil {
		return "", err
	}
	// 始终引用完整绝对路径，避免中文、空格及 Windows 启动命令解析歧义。
	return `"` + exe + `" tray --config "` + config + `"`, nil
}

func desiredStartup(configPath string) (name, command string, err error) {
	name, err = configIdentity(configPath)
	if err != nil {
		return
	}
	var exe string
	exe, err = os.Executable()
	if err != nil {
		return
	}
	command, err = startupCommand(exe, configPath)
	return
}

func StartupEnabled(configPath string) (bool, error) {
	name, want, err := desiredStartup(configPath)
	if err != nil {
		return false, err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	got, _, err := key.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == want, nil
}

// 此函数仅供用户明确点击自启动开关时调用，Run 不会自动写入注册表。
func SetStartup(enabled bool, configPath string) error {
	name, command, err := desiredStartup(configPath)
	if err != nil {
		return err
	}
	var key registry.Key
	if enabled {
		key, _, err = registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	} else {
		key, err = registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	defer key.Close()
	if enabled {
		return key.SetStringValue(name, command)
	}
	err = key.DeleteValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

func wide(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(strings.ReplaceAll(s, "\x00", ""))
	return p
}

func openPath(path string) error {
	if path == "" {
		return errors.New("尚未配置文件路径")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	verb, file := wide("open"), wide(absolute)
	result, _, _ := shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, 1)
	runtime.KeepAlive(verb)
	runtime.KeepAlive(file)
	if result <= 32 {
		return fmt.Errorf("无法打开文件 (Shell code %d)", result)
	}
	return nil
}

func interactiveDesktop() bool {
	station, _, _ := getWindowStation.Call()
	if station == 0 {
		return false
	}
	var flags struct {
		Inherit, Reserved int32
		Flags             uint32
	}
	var needed uint32
	result, _, _ := getUserObjectInformation.Call(station, 1, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags), uintptr(unsafe.Pointer(&needed)))
	return result != 0 && flags.Flags&1 != 0
}

func ErrorDialog(message string) {
	if !interactiveDesktop() {
		fmt.Fprintln(os.Stderr, message)
		return
	}
	text, title := wide(message), wide("校园网保活")
	messageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
	runtime.KeepAlive(text)
	runtime.KeepAlive(title)
}

func IsDoubleClick() bool {
	var processes [8]uint32
	count, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&processes[0])), uintptr(len(processes)))
	return count == 1
}

func hideStandaloneConsole() {
	var processes [8]uint32
	count, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&processes[0])), uintptr(len(processes)))
	handle, _, _ := getConsoleWindow.Call()
	// 双击或登录启动创建的独立控制台可隐藏；未知状态和父 PowerShell 绝不隐藏。
	if shouldHideConsole(count, handle) {
		showWindow.Call(handle, 0)
	}
}

type trayWindow struct {
	handle         windows.Handle
	options        Options
	dispatch       *dispatcher
	results        chan error
	taskbarCreated uint32
	destroyed      bool
	iconAdded      bool
	messagePump    bool
}

var nativeWindows = struct {
	sync.RWMutex
	items map[windows.Handle]*trayWindow
}{items: make(map[windows.Handle]*trayWindow)}
var classOnce sync.Once
var classErr error
var nativeCallback = syscall.NewCallback(windowProc)

func registerNativeClass() error {
	classOnce.Do(func() {
		name := wide(windowClass)
		module, _, _ := getModuleHandle.Call(0)
		class := wndClassEx{CbSize: uint32(unsafe.Sizeof(wndClassEx{})), LpfnWndProc: nativeCallback, HInstance: windows.Handle(module), LpszClassName: name}
		result, _, err := registerClass.Call(uintptr(unsafe.Pointer(&class)))
		runtime.KeepAlive(name)
		if result == 0 {
			classErr = fmt.Errorf("注册隐藏窗口失败: %w", err)
		}
	})
	return classErr
}

func post(handle windows.Handle, message uint32) {
	postMessage.Call(uintptr(handle), uintptr(message), 0, 0)
}

// 所有窗口创建、托盘修改、菜单与 GetMessage 都在同一个锁定的 OS 线程上。
// 使用无 WS_VISIBLE 的顶层窗口，而非 HWND_MESSAGE，才能收到 Explorer 广播。
func Run(ctx context.Context, opts Options) error {
	if ctx.Err() != nil {
		return nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hideStandaloneConsole()
	if err := registerNativeClass(); err != nil {
		return err
	}
	class, title := wide(windowClass), wide("校园网保活消息窗口")
	module, _, _ := getModuleHandle.Call(0)
	handle, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0, 0, 0, 0, 0, 0, 0, module, 0)
	runtime.KeepAlive(class)
	runtime.KeepAlive(title)
	if handle == 0 {
		return fmt.Errorf("创建隐藏窗口失败: %w", err)
	}
	taskbar := wide("TaskbarCreated")
	taskbarID, _, _ := regWindowMessage.Call(uintptr(unsafe.Pointer(taskbar)))
	runtime.KeepAlive(taskbar)
	w := &trayWindow{handle: windows.Handle(handle), options: opts, results: make(chan error, 32), taskbarCreated: uint32(taskbarID)}
	nativeWindows.Lock()
	nativeWindows.items[w.handle] = w
	nativeWindows.Unlock()
	defer func() {
		w.messagePump = false
		killTimer.Call(handle, 1)
		if w.iconAdded {
			w.notify(nimDelete, Status{}, "")
		}
		if !w.destroyed {
			destroyWindow.Call(handle)
		}
		nativeWindows.Lock()
		delete(nativeWindows.items, w.handle)
		nativeWindows.Unlock()
	}()
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	workerOpts := opts
	workerOpts.Quit = func() {
		if opts.Quit != nil {
			opts.Quit()
		}
		post(w.handle, wmClose)
	}
	w.dispatch = newDispatcher(child, workerOpts, func(err error) {
		select {
		case <-child.Done():
			return
		case w.results <- err:
			post(w.handle, wmResult)
		}
	})
	if !w.notify(nimAdd, w.snapshot(), "") {
		return errors.New("无法添加 Windows 托盘图标，请确认桌面任务栏正在运行")
	}
	w.iconAdded = true
	timer, _, err := setTimer.Call(handle, 1, 1000, 0)
	if timer == 0 {
		return fmt.Errorf("启动托盘状态计时器失败: %w", err)
	}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			post(w.handle, wmClose)
		case <-stopped:
		}
	}()
	w.messagePump = true
	for {
		var message msgStruct
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) == -1 {
			return fmt.Errorf("读取 Windows 消息失败: %w", err)
		}
		if result == 0 {
			return nil
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&message)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func (w *trayWindow) snapshot() Status {
	if w.options.Snapshot == nil {
		return Status{State: "starting"}
	}
	return w.options.Snapshot()
}

func windowProc(handle uintptr, message uint32, wparam, lparam uintptr) uintptr {
	nativeWindows.RLock()
	w := nativeWindows.items[windows.Handle(handle)]
	nativeWindows.RUnlock()
	if w != nil {
		if w.taskbarCreated != 0 && message == w.taskbarCreated {
			w.iconAdded = w.notify(nimAdd, w.snapshot(), "")
			return 0
		}
		switch message {
		case wmTray:
			switch uint32(lparam) & 0xffff {
			case wmRButtonUp, wmLButtonUp, wmContextMenu:
				w.menu()
			}
			return 0
		case wmCommand:
			w.command(int(wparam & 0xffff))
			return 0
		case wmTimer:
			action := uintptr(nimModify)
			if !w.iconAdded {
				action = nimAdd
			}
			w.iconAdded = w.notify(action, w.snapshot(), "")
			return 0
		case wmResult:
			for {
				select {
				case err := <-w.results:
					detail := ""
					if err != nil {
						detail = "操作失败，请检查配置与日志后重试。"
					}
					w.notify(nimModify, w.snapshot(), detail)
				default:
					return 0
				}
			}
		case wmClose:
			destroyWindow.Call(handle)
			return 0
		case wmDestroy:
			w.destroyed = true
			// 初始化中途失败时还没有消息循环；不可遗留 WM_QUIT 污染可复用线程。
			if w.messagePump {
				postQuitMessage.Call(0)
			}
			return 0
		}
	}
	result, _, _ := defWindowProc.Call(handle, uintptr(message), wparam, lparam)
	return result
}

func fillWide(destination []uint16, text string) {
	encoded := utf16.Encode([]rune(strings.ReplaceAll(text, "\x00", "")))
	if len(encoded) >= len(destination) {
		encoded = encoded[:len(destination)-1]
		if len(encoded) > 0 && encoded[len(encoded)-1] >= 0xd800 && encoded[len(encoded)-1] <= 0xdbff {
			encoded = encoded[:len(encoded)-1]
		}
	}
	clear(destination)
	copy(destination, encoded)
}

func statusLabel(s Status) string {
	if s.Paused {
		return "已暂停"
	}
	switch s.State {
	case "online", "在线":
		return "在线"
	case "authenticating", "认证中":
		return "认证中"
	case "cooldown", "重试冷却":
		return "冷却等待"
	case "credentials_error", "credential_error", "账号配置错误":
		return "账号认证失败"
	case "paused", "已暂停":
		return "已暂停"
	case "offline", "pending", "pending_auth", "waiting", "待认证":
		return "等待联网"
	case "网络故障":
		return "网络故障"
	default:
		return "正在检测"
	}
}

func statusIcon(s Status) uintptr {
	iconID := uintptr(32515)
	if s.State == "online" || s.State == "在线" {
		iconID = 32516
	}
	if strings.Contains(s.State, "credential") || s.State == "账号配置错误" {
		iconID = 32513
	}
	if s.Paused || s.State == "paused" || s.State == "已暂停" {
		iconID = 32512
	}
	return iconID
}

func shouldHideConsole(count, handle uintptr) bool { return count == 1 && handle != 0 }

func (w *trayWindow) notify(action uintptr, s Status, information string) bool {
	iconID := statusIcon(s)
	icon, _, _ := loadIcon.Call(0, iconID)
	data := notifyIconData{CbSize: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: w.handle, UID: 1, UFlags: nifMessage | nifIcon | nifTip, UCallbackMessage: wmTray, HIcon: windows.Handle(icon)}
	// 只展示状态枚举，不把可能包含网络地址/账号的详细消息复制进系统提示。
	fillWide(data.SzTip[:], "校园网保活："+statusLabel(s))
	if information != "" {
		data.UFlags |= nifInfo
		data.DwInfoFlags = 2
		fillWide(data.SzInfo[:], information)
		fillWide(data.SzInfoTitle[:], "校园网保活")
	}
	result, _, _ := shellNotifyIcon.Call(action, uintptr(unsafe.Pointer(&data)))
	return result != 0
}

func (w *trayWindow) command(id int) {
	if !w.dispatch.enqueue(id, w.snapshot()) {
		w.notify(nimModify, w.snapshot(), "操作正在等待处理，请稍后重试。")
	}
}

func (w *trayWindow) menu() {
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		return
	}
	defer destroyMenu.Call(menu)
	add := func(id int, label string, flags uintptr) {
		text := wide(label)
		appendMenu.Call(menu, flags, uintptr(id), uintptr(unsafe.Pointer(text)))
		runtime.KeepAlive(text)
	}
	s := w.snapshot()
	add(0, "状态："+statusLabel(s), mfGrayed)
	add(0, "", mfSeparator)
	pause := "暂停"
	if s.Paused || s.State == "paused" || s.State == "已暂停" {
		pause = "恢复"
		s.Paused = true
	}
	add(cmdPause, pause, 0)
	add(cmdRetry, "立即重试", 0)
	add(cmdReload, "重载配置", 0)
	add(cmdConfig, "打开配置", 0)
	add(cmdLog, "打开日志", 0)
	enabled, err := StartupEnabled(w.options.ConfigPath)
	flags := uintptr(0)
	if enabled {
		flags = mfChecked
	}
	if err != nil {
		flags |= mfGrayed
	}
	add(cmdStartup, "开机启动", flags)
	add(0, "", mfSeparator)
	add(cmdQuit, "退出", 0)
	var position point
	getCursorPos.Call(uintptr(unsafe.Pointer(&position)))
	setForeground.Call(uintptr(w.handle))
	chosen, _, _ := trackPopupMenu.Call(menu, 0x102, uintptr(position.X), uintptr(position.Y), 0, uintptr(w.handle), 0)
	// TrackPopupMenu 返回命令 ID，由当前线程入队；wmNull 满足系统菜单收起规则。
	if chosen != 0 {
		if !w.dispatch.enqueue(int(chosen), s) {
			w.notify(nimModify, s, "操作正在等待处理，请稍后重试。")
		}
	}
	post(w.handle, 0)
}
