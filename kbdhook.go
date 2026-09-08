package main

import (
	goruntime "runtime"
	"syscall"
	"unsafe"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Focus-scoped Win+, accelerator via a low-level keyboard hook. When the
// Translator window itself is foreground, Win+, toggles the settings pane
// and the event is swallowed so the shell's desktop-peek never fires; in
// every other app the key passes straight through and Peek keeps working.
// Win+T (global) does NOT go through here — RegisterHotKey handles it.
//
// The hook lives on its own pinned thread with a message pump: low-level
// hook callbacks must return within a few ms or Windows silently removes
// the hook, and the hotkey thread regularly spends up to the capture
// deadline without pumping. The callback itself never blocks (the event
// emit is a queue-append + PostMessage on the UI thread).

type kbdllhookstruct struct {
	vkCode      uint32
	scanCode    uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

var appRef *App

func (a *App) startKbdHook() {
	appRef = a
	go func() {
		goruntime.LockOSThread()
		defer goruntime.UnlockOSThread()

		var msg [48]byte // MSG on 64-bit Windows
		// Open this thread's message queue; LL callbacks arrive while it pumps.
		procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0, 0)

		h, _, _ := procSetWindowsHookExW.Call(whKeyboardLl, kbdHookProc, 0, 0)
		if h == 0 {
			wailsruntime.LogErrorf(a.ctx, "Win+, hook could not be installed; settings shortcut disabled.")
			return
		}
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
			if int32(r) == 0 || int32(r) == -1 {
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg[0])))
		}
	}()
}

var kbdHookProc = syscall.NewCallback(func(nCode uintptr, wParam uintptr, lParam uintptr) uintptr {
	if int32(nCode) >= 0 {
		msg := uint32(wParam)
		if msg == wmKeydown || msg == wmSyskeydown || msg == wmKeyup || msg == wmSyskeyup {
			// Copy the KBDLLHOOKSTRUCT via RtlMoveMemory instead of converting
			// lParam (a raw uintptr) into a Go pointer — same pattern as
			// clipboard.go, and it keeps `go vet` happy.
			var kb kbdllhookstruct
			procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&kb)), lParam, unsafe.Sizeof(kb))
			// Skip our own synthesized input; only react to real Win+,
			// pressed while our window is foreground.
			if kb.vkCode == vkOemComma && kb.flags&llkhfInjected == 0 && winKeyDown() && translatorIsForeground() {
				if appRef != nil && appRef.ctx != nil {
					wailsruntime.EventsEmit(appRef.ctx, "open-settings")
				}
				return 1 // swallow: shell desktop-peek must not fire
			}
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return r
})

func winKeyDown() bool {
	r1, _, _ := procGetAsyncKeyState.Call(vkLwin)
	r2, _, _ := procGetAsyncKeyState.Call(vkRwin)
	return r1&0x8000 != 0 || r2&0x8000 != 0
}

var cachedTranslatorHwnd uintptr

func translatorIsForeground() bool {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	if cachedTranslatorHwnd == 0 {
		cachedTranslatorHwnd = findTranslatorWindow()
	}
	return fg == cachedTranslatorHwnd
}
