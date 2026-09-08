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
// Trigger is dual-path: the keyboard hook in kbdhook.go is PRIMARY (the
// shell owns Win+T, RegisterHotKey fails with "already registered") and
// posts wmAppHotkey here; our own RegisterHotKey attempt is a fallback for
// machines where the combo happens to be free.
//
// Order on WM_HOTKEY (do not reorder — avoids the focus-stealing race):
//  1. capture the selection FIRST (previous app still focused)
//  2. show + foreground our window
//  3. emit "hotkey-pressed" (frontend clears the input)
//  4. emit "prefill" with the captured text
//
// No COM is initialized on this thread: capture is clipboard-only (see
// selection.go) and UIA was removed after it wedged the whole process.

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

		// Best-effort: the shell owns Win+T on most machines ("Hot key is
		// already registered"), so the primary trigger is the keyboard hook
		// in kbdhook.go posting wmAppHotkey here. If registration happens to
		// succeed, WM_HOTKEY works as a fallback. Either way this thread
		// must stay alive and pump.
		procRegisterHotKey.Call(0, hotkeyID, modWin|modNoRepeat, vkT)
		id, _, _ := procGetCurrentThreadId.Call()
		hotkeyThreadId = id

		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
			if int32(r) == 0 || int32(r) == -1 {
				return // WM_QUIT or error
			}
			msgID := *(*uint32)(unsafe.Pointer(&msg[8]))
			wParam := *(*uintptr)(unsafe.Pointer(&msg[16]))
			if (msgID == wmHotkey && int(wParam) == hotkeyID) || msgID == wmAppHotkey {
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
// window. Called after capture, from the hotkey thread.
//
// The hook-based trigger swallows Win+T before it reaches any input queue,
// so this thread has not "received the last input event" and a plain
// SetForegroundWindow silently fails while another app owns the foreground
// (the window would pop up BEHIND it). Waivers, least side effects first:
//  1. AttachThreadInput to the current foreground thread briefly
//  2. a quick synthesized Alt press (menu-key foreground waiver) + retry
func foregroundTranslatorWindow(hwnd uintptr) {
	r, _, _ := procIsIconic.Call(hwnd)
	if r != 0 {
		procShowWindow.Call(hwnd, swRestore)
	} else {
		procShowWindow.Call(hwnd, swShow)
	}

	fg, _, _ := procGetForegroundWindow.Call()
	if fg == hwnd {
		return
	}

	var fgThread uintptr
	if fg != 0 {
		fgThread, _, _ = procGetWindowThreadProcessId.Call(fg, 0)
	}
	thisThread, _, _ := procGetCurrentThreadId.Call()

	attached := uintptr(0)
	if fgThread != 0 && fgThread != thisThread {
		attached, _, _ = procAttachThreadInput.Call(thisThread, fgThread, 1)
	}

	ok, _, _ := procSetForegroundWindow.Call(hwnd)
	if ok == 0 {
		sendKey(vkMenu, false)
		sendKey(vkMenu, true)
		procSetForegroundWindow.Call(hwnd)
	}

	if attached != 0 {
		procAttachThreadInput.Call(thisThread, fgThread, 0)
	}
}
