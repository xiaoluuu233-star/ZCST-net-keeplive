//go:build windows

package winui

// Win32 结构布局与少量绑定参考 stmtc233/zcst-net (MIT)，固定提交
// eb928170bb23d08d2331de9517743190c63b2e12；完整许可见 THIRD_PARTY_NOTICES.md。
import (
	"golang.org/x/sys/windows"
)

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	shell32                  = windows.NewLazySystemDLL("shell32.dll")
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	registerClass            = user32.NewProc("RegisterClassExW")
	createWindow             = user32.NewProc("CreateWindowExW")
	defWindowProc            = user32.NewProc("DefWindowProcW")
	destroyWindow            = user32.NewProc("DestroyWindow")
	getMessage               = user32.NewProc("GetMessageW")
	translateMessage         = user32.NewProc("TranslateMessage")
	dispatchMessage          = user32.NewProc("DispatchMessageW")
	postQuitMessage          = user32.NewProc("PostQuitMessage")
	postMessage              = user32.NewProc("PostMessageW")
	setForeground            = user32.NewProc("SetForegroundWindow")
	createPopupMenu          = user32.NewProc("CreatePopupMenu")
	appendMenu               = user32.NewProc("AppendMenuW")
	destroyMenu              = user32.NewProc("DestroyMenu")
	trackPopupMenu           = user32.NewProc("TrackPopupMenu")
	getCursorPos             = user32.NewProc("GetCursorPos")
	messageBox               = user32.NewProc("MessageBoxW")
	setTimer                 = user32.NewProc("SetTimer")
	killTimer                = user32.NewProc("KillTimer")
	loadIcon                 = user32.NewProc("LoadIconW")
	regWindowMessage         = user32.NewProc("RegisterWindowMessageW")
	getWindowStation         = user32.NewProc("GetProcessWindowStation")
	getUserObjectInformation = user32.NewProc("GetUserObjectInformationW")
	shellNotifyIcon          = shell32.NewProc("Shell_NotifyIconW")
	shellExecute             = shell32.NewProc("ShellExecuteW")
	getModuleHandle          = kernel32.NewProc("GetModuleHandleW")
	getConsoleProcessList    = kernel32.NewProc("GetConsoleProcessList")
	getConsoleWindow         = kernel32.NewProc("GetConsoleWindow")
	showWindow               = user32.NewProc("ShowWindow")
)

const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmCommand     = 0x0111
	wmTimer       = 0x0113
	wmTray        = 0x8001
	wmResult      = 0x8002
	wmContextMenu = 0x007b
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nifMessage    = 1
	nifIcon       = 2
	nifTip        = 4
	nifInfo       = 16
	mfGrayed      = 1
	mfChecked     = 8
	mfSeparator   = 0x800
)

type point struct{ X, Y int32 }
type wndClassEx struct {
	CbSize, Style                            uint32
	LpfnWndProc                              uintptr
	CbClsExtra, CbWndExtra                   int32
	HInstance, HIcon, HCursor, HbrBackground windows.Handle
	LpszMenuName, LpszClassName              *uint16
	HIconSm                                  windows.Handle
}

// Win64 MSG 尾部 lPrivate 不可省略；Go 自然对齐使总长为 48 字节。
type msgStruct struct {
	HWnd           windows.Handle
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             point
	LPrivate       uint32
}

// NOTIFYICONDATAW 的 timeout/version 是 union，只能占一个 uint32。
// amd64 大小 976，HIcon@32、SzTip@40、GuidItem@952、HBalloonIcon@968。
type notifyIconData struct {
	CbSize                        uint32
	HWnd                          windows.Handle
	UID, UFlags, UCallbackMessage uint32
	HIcon                         windows.Handle
	SzTip                         [128]uint16
	DwState, DwStateMask          uint32
	SzInfo                        [256]uint16
	UVersion                      uint32
	SzInfoTitle                   [64]uint16
	DwInfoFlags                   uint32
	GuidItem                      [16]byte
	HBalloonIcon                  windows.Handle
}
