package main

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Raw Win32 clipboard access (CF_UNICODETEXT). No cgo, no external clipboard
// library. OpenClipboard is retried: another app may hold it briefly.

func openClipboard() bool {
	for i := 0; i < 10; i++ {
		r, _, _ := procOpenClipboard.Call(0)
		if r != 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func setClipboardText(text string) error {
	if text == "" {
		return nil
	}
	u16, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	size := uintptr(len(u16)) * 2
	if !openClipboard() {
		return errors.New("could not open the clipboard")
	}
	defer procCloseClipboard.Call()

	r, _, _ := procEmptyClipboard.Call()
	if r == 0 {
		return errors.New("could not empty the clipboard")
	}
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return errors.New("could not allocate clipboard memory")
	}
	owned := false
	defer func() {
		if !owned {
			procGlobalFree.Call(h)
		}
	}()
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return errors.New("could not lock clipboard memory")
	}
	// Copy via RtlMoveMemory so no raw address is dereferenced in Go code.
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u16[0])), size)
	procGlobalUnlock.Call(h)

	// On success the system owns the handle: do not free it.
	r, _, _ = procSetClipboardData.Call(cfUnicodetext, h)
	if r == 0 {
		return errors.New("could not set clipboard data")
	}
	owned = true
	return nil
}

func getClipboardText() (string, bool) {
	r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodetext)
	if r == 0 {
		return "", false
	}
	if !openClipboard() {
		return "", false
	}
	defer procCloseClipboard.Call()

	h, _, _ := procGetClipboardData.Call(cfUnicodetext)
	if h == 0 {
		return "", false
	}
	sz, _, _ := procGlobalSize.Call(h)
	if sz < 2 {
		return "", false
	}
	if sz > 1<<27 {
		return "", false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)

	n := int(sz / 2)
	if n > 1<<26 {
		n = 1 << 26
	}
	if n <= 0 {
		return "", false
	}
	buf := make([]uint16, n)
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), p, uintptr(n*2))
	return windows.UTF16ToString(buf), true
}

// getClipboardSequenceNumber returns the system clipboard sequence number
// (0 on failure). It bumps on every clipboard content change, no open needed
// — used to detect that the synthesized Ctrl+C actually copied something.
func getClipboardSequenceNumber() uint32 {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(r)
}
