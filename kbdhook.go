package main

import (
	goruntime "runtime"
	"syscall"
	"unsafe"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Low-level keyboard hook, two jobs:
//
//   - Win+T (global): the shell owns Win+T — RegisterHotKey(MOD_WIN, T) fails
//     with "Hot key is already registered" (probed live; MOD_ALT succeeds) —
//     so the only way in is to swallow the key here, before any hotkey
//     mechanism sees it, and post wmAppHotkey to the hotkey thread. The
//     swallowed key also takes the shell's taskbar-cycling out of play.
//   - Win+, (focus-scoped): toggles the settings pane ONLY while the
//     Translator window is foreground; in every other app the key passes
//     straight through and the shell's desktop-peek keeps working.
//
// After swallowing a Win-combo we mask the next Win key-up: the shell opens
// the Start menu on a Win release it believes had "no key pressed in
// between" (the AutoHotkey masking pattern).
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

var (
	appRef *App

	// Set by the hotkey thread so the hook can post wmAppHotkey to it.
	hotkeyThreadId uintptr

	// Hook-thread-only state (the callback is serialized on that thread).
	winTArmed         bool // Win+T currently held: swallow auto-repeats
	winComboSwallowed bool // mask the next Win key-up (Start menu)
)

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
			// Skip our own synthesized input entirely.
			if kb.flags&llkhfInjected == 0 {
				switch kb.vkCode {
				case vkT:
					if winKeyDown() {
						if kb.flags&llkhfUp != 0 {
							winTArmed = false
						} else if !winTArmed {
							winTArmed = true
							winComboSwallowed = true
							sendMenuMaskKey()
							if hotkeyThreadId != 0 {
								procPostThreadMessageW.Call(hotkeyThreadId, wmAppHotkey, 0, 0)
							}
						}
						return 1 // swallow: the shell's taskbar-cycle must not fire
					}
				case vkLwin, vkRwin:
					// Mask the Win release after a swallowed combo so the
					// shell does not pop the Start menu on a release it
					// believes had no key pressed in between.
					if kb.flags&llkhfUp != 0 && winComboSwallowed {
						winComboSwallowed = false
						return 1
					}
				case vkOemComma:
					if winKeyDown() && translatorIsForeground() {
						if kb.flags&llkhfUp == 0 {
							winComboSwallowed = true
							sendMenuMaskKey()
							if appRef != nil && appRef.ctx != nil {
								wailsruntime.EventsEmit(appRef.ctx, "open-settings")
							}
						}
						return 1 // swallow: shell desktop-peek must not fire
					}
				}
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

// sendMenuMaskKey injects a quick tap of an unassigned key right after we
// swallow a Win-combo. The Start-menu decision happens below the LL hook
// chain, so merely swallowing the Win key-up is not enough; the shell must
// SEE some key being used while Win is held (AHK #MenuMaskKey pattern).
// Our own hook skips it (INJECTED flag) and passes it downstream.
func sendMenuMaskKey() {
	sendKey(vkMenuMask, false)
	sendKey(vkMenuMask, true)
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
