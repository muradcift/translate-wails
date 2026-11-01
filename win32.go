package main

import "golang.org/x/sys/windows"

// Raw Win32 entry points used by the backend. Everything goes through
// golang.org/x/sys/windows (pure Go, no cgo, no gcc).

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modWinHTTP  = windows.NewLazySystemDLL("winhttp.dll")
	modDwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	procRegisterHotKey             = modUser32.NewProc("RegisterHotKey")
	procGetMessageW                = modUser32.NewProc("GetMessageW")
	procPeekMessageW               = modUser32.NewProc("PeekMessageW")
	procTranslateMessage           = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW           = modUser32.NewProc("DispatchMessageW")
	procShowWindow                 = modUser32.NewProc("ShowWindow")
	procIsIconic                   = modUser32.NewProc("IsIconic")
	procSetForegroundWindow        = modUser32.NewProc("SetForegroundWindow")
	procEnumWindows                = modUser32.NewProc("EnumWindows")
	procGetWindowThreadProcessId   = modUser32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW             = modUser32.NewProc("GetWindowTextW")
	procSetWindowLongPtrW          = modUser32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtrW          = modUser32.NewProc("GetWindowLongPtrW")
	procSetWindowPos               = modUser32.NewProc("SetWindowPos")
	procSendInput                  = modUser32.NewProc("SendInput")
	procSetWindowsHookExW          = modUser32.NewProc("SetWindowsHookExW")
	procCallNextHookEx             = modUser32.NewProc("CallNextHookEx")
	procPostThreadMessageW         = modUser32.NewProc("PostThreadMessageW")
	procAttachThreadInput          = modUser32.NewProc("AttachThreadInput")
	procGetAsyncKeyState           = modUser32.NewProc("GetAsyncKeyState")
	procGetForegroundWindow        = modUser32.NewProc("GetForegroundWindow")
	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard             = modUser32.NewProc("EmptyClipboard")
	procSetClipboardData           = modUser32.NewProc("SetClipboardData")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")
	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber = modUser32.NewProc("GetClipboardSequenceNumber")
	procIsWindowVisible            = modUser32.NewProc("IsWindowVisible")

	procGlobalAlloc        = modKernel32.NewProc("GlobalAlloc")
	procGlobalFree         = modKernel32.NewProc("GlobalFree")
	procGlobalLock         = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock       = modKernel32.NewProc("GlobalUnlock")
	procGlobalSize         = modKernel32.NewProc("GlobalSize")
	procRtlMoveMemory      = modKernel32.NewProc("RtlMoveMemory")
	procGetCurrentThreadId = modKernel32.NewProc("GetCurrentThreadId")

	procDwmSetWindowAttribute = modDwmapi.NewProc("DwmSetWindowAttribute")

	procWinHttpOpen               = modWinHTTP.NewProc("WinHttpOpen")
	procWinHttpConnect            = modWinHTTP.NewProc("WinHttpConnect")
	procWinHttpOpenRequest        = modWinHTTP.NewProc("WinHttpOpenRequest")
	procWinHttpAddRequestHeaders  = modWinHTTP.NewProc("WinHttpAddRequestHeaders")
	procWinHttpSendRequest        = modWinHTTP.NewProc("WinHttpSendRequest")
	procWinHttpReceiveResponse    = modWinHTTP.NewProc("WinHttpReceiveResponse")
	procWinHttpQueryHeaders       = modWinHTTP.NewProc("WinHttpQueryHeaders")
	procWinHttpQueryDataAvailable = modWinHTTP.NewProc("WinHttpQueryDataAvailable")
	procWinHttpReadData           = modWinHTTP.NewProc("WinHttpReadData")
	procWinHttpSetTimeouts        = modWinHTTP.NewProc("WinHttpSetTimeouts")
	procWinHttpSetOption          = modWinHTTP.NewProc("WinHttpSetOption")
	procWinHttpCloseHandle        = modWinHTTP.NewProc("WinHttpCloseHandle")
)

const (
	modWin      = 0x0008
	modNoRepeat = 0x4000
	vkT         = 0x54
	wmHotkey    = 0x0312

	// Low-level keyboard hook (WH_KEYBOARD_LL): Win+T trigger and the
	// focus-scoped Win+, settings accelerator. See kbdhook.go.
	whKeyboardLl  = 13
	llkhfInjected = 0x10
	llkhfUp       = 0x80
	wmKeydown     = 0x0100
	wmKeyup       = 0x0101
	wmSyskeydown  = 0x0104
	wmSyskeyup    = 0x0105
	wmAppHotkey   = 0x8001 // WM_APP+1: hook-triggered Win+T -> hotkey thread

	// Windows 11 rounded corners via DWM; the call is a silent no-op on Win10.
	dwmwaWindowCornerPreference = 33
	dwmwcpRound                 = 2

	swShow    = 5
	swShowNA  = 4
	swRestore = 9

	gwlExstyle      = 0xFFFFFFEC
	wsExToolwindow  = 0x80
	swpNosize       = 0x0001
	swpNomove       = 0x0002
	swpNozorder     = 0x0004
	swpNoactivate   = 0x0010
	swpFramechanged = 0x0020

	cfUnicodetext = 13
	gmemMoveable  = 0x0002

	vkControl  = 0x11
	vkMenu     = 0x12
	vkShift    = 0x10
	vkLwin     = 0x5B
	vkRwin     = 0x5C
	vkOemComma = 0xBC
	vkC        = 0x43
	// Unassigned VK used as the menu-mask dummy (AHK #MenuMaskKey pattern):
	// injected after a swallowed Win-combo so the shell sees "a key was used
	// while Win was held" and does not pop the Start menu on Win release.
	vkMenuMask     = 0xE8
	keyeventfKeyup = 0x0002
	inputKeyboard  = 1
	inputSize      = 40 // sizeof(INPUT) on 64-bit Windows

	winhttpAccessAutomaticProxy = 4
	winhttpDefaultHTTPSPort     = 443
	winhttpFlagSecure           = 0x00800000
	winhttpAddreqAddIfNew       = 0x10000000
	winhttpQueryStatusCode      = 19
	winhttpQueryFlagNumber      = 0x20000000

	maxHTTPBody = 8 << 20
)
