package main

import (
	"os"
	goruntime "runtime"
	"syscall"
	"time"
	"unsafe"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

// Win+T global hotkey. The handler runs synchronously on the registering
// thread (pinned with LockOSThread + its own GetMessage loop), because that
// thread owns the foreground activation right from the hotkey input.
//
// Order on WM_HOTKEY (do not reorder — avoids the focus-stealing race):
//  1. capture the selection FIRST (previous app still focused)
//  2. show + foreground our window
//  3. emit "hotkey-pressed" (frontend clears the input)
//  4. emit "prefill" with the captured text
//
// No COM is initialized on this thread: capture is clipboard-only (see
// selection.go) and UIA was removed after it wedged the whole process.
// MOD_WIN suppresses the system's Win+T taskbar-cycling shortcut.

const hotkeyID = 1

// captureDeadline bounds the wait for captureSelection so the window always
// comes to front even if a clipboard owner wedges the read. A late result is
// discarded (only this thread emits prefill), so nothing can clobber the
// input after the user started typing.
const captureDeadline = 1200 * time.Millisecond

func (a *App) startHotkeyLoop() {
	go func() {
		goruntime.LockOSThread()
		defer goruntime.UnlockOSThread()

		var msg [48]byte // MSG on 64-bit Windows
		// Open this thread's message queue before registering.
		procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0, 0)

		r, _, _ := procRegisterHotKey.Call(0, hotkeyID, modWin|modNoRepeat, vkT)
		if r == 0 {
			wailsruntime.LogErrorf(a.ctx, "Win+T hotkey could not be registered (already in use?).")
			return
		}

		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
			if int32(r) == 0 || int32(r) == -1 {
				return // WM_QUIT or error
			}
			msgID := *(*uint32)(unsafe.Pointer(&msg[8]))
			wParam := *(*uintptr)(unsafe.Pointer(&msg[16]))
			if msgID == wmHotkey && int(wParam) == hotkeyID {
				a.handleHotkey()
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg[0])))
		}
	}()
}

// Order on WM_HOTKEY: show INSTANTLY without activating (SW_SHOWNA steals
// no focus, so the previous app stays foreground for capture), capture,
// then foreground + prefill. Never show-activating before capture — the
// clipboard Ctrl+C must land in the previous app.
func (a *App) handleHotkey() {
	// (a) Instant visual feedback, zero focus change.
	hwnd := findTranslatorWindow()
	if hwnd != 0 {
		hideFromTaskbar(hwnd)
		procShowWindow.Call(hwnd, swShowNA)
	}

	// Between show and prefill: frontend flags that a selection is being read.
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "hotkey-pressed")
	}

	// (b) Selection while the previous app still has focus. Bounded by the
	// deadline: a wedged clipboard owner can hold the capture goroutine, but
	// the window still comes to front and the late result is dropped.
	ch := make(chan string, 1)
	go func() { ch <- captureSelection() }()
	var text string
	select {
	case text = <-ch:
	case <-time.After(captureDeadline):
	}

	// (c) Then foreground our window and deliver the text — unless the user
	// dismissed it with Esc while the capture was running: a window the user
	// hid must not resurrect itself in front of them.
	if hwnd != 0 && isWindowVisible(hwnd) {
		foregroundTranslatorWindow(hwnd)
	}
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "prefill", text)
	}
}

// hideFromTaskbar removes our taskbar button (WS_EX_TOOLWINDOW, applied
// once): no taskbar flash, no tray-origin show animation, no Alt+Tab entry
// — Raycast-style presence.
var toolwindowApplied = false

func hideFromTaskbar(hwnd uintptr) {
	if toolwindowApplied {
		return
	}
	toolwindowApplied = true
	r, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlExstyle)
	procSetWindowLongPtrW.Call(hwnd, gwlExstyle, r|wsExToolwindow)
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNoactivate|swpNomove|swpNosize|swpNozorder|swpFramechanged)
}

// Package-level EnumWindows callback: syscall.NewCallback has a process-wide
// cap (~2000), so it must not be created per call.
var (
	enumTarget    uintptr
	enumWindowsCb = syscall.NewCallback(enumTranslatorWindowProc)
	enumMyPid     = uint32(os.Getpid())
	enumTitleBuf  [256]uint16
)

func enumTranslatorWindowProc(hwnd uintptr, _ uintptr) uintptr {
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != enumMyPid {
		return 1
	}
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&enumTitleBuf[0])), 256)
	if n == 0 {
		return 1
	}
	if windows.UTF16ToString(enumTitleBuf[:n]) == "Translator" {
		enumTarget = hwnd
		return 0
	}
	return 1
}

// findTranslatorWindow finds our own "Translator" window via EnumWindows.
func findTranslatorWindow() uintptr {
	enumTarget = 0
	procEnumWindows.Call(enumWindowsCb, 0)
	return enumTarget
}

// isWindowVisible reports whether our window is currently on screen; used to
// skip foregrounding when the user Esc'd the window away mid-capture.
func isWindowVisible(hwnd uintptr) bool {
	r, _, _ := procIsWindowVisible.Call(hwnd)
	return r != 0
}

// foregroundTranslatorWindow restores (if needed) and foregrounds our
// window. Called after capture, from the hotkey thread that owns the
// foreground activation right.
func foregroundTranslatorWindow(hwnd uintptr) {
	r, _, _ := procIsIconic.Call(hwnd)
	if r != 0 {
		procShowWindow.Call(hwnd, swRestore)
	} else {
		procShowWindow.Call(hwnd, swShow)
	}
	procSetForegroundWindow.Call(hwnd)
}
