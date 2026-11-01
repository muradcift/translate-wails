package main

import (
	"encoding/binary"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"
)

// Selected-text capture, clipboard-only by design: snapshot the clipboard,
// synthesize Ctrl+C into the still-foreground previous app, read, restore.
//
// UIA (TextPattern via raw COM vtables) was tried and removed on purpose:
// real cross-process UIA calls have no timeout, can wedge the hotkey thread
// on a busy provider, and drag the Wails UI thread into cross-apartment
// servicing (observed live: window never focuses, "not responding", WebView2
// browser process exits and Wails kills the process). If UIA is ever
// revisited it must run on its own throwaway thread with a hard deadline.
// No COM anywhere on this path.

const maxSelectionChars = 5000

// How long to wait for the synthesized Ctrl+C to actually change the
// clipboard (sequence-number poll). Fast apps land in a few ms.
const copyPollWindow = 400 * time.Millisecond

func captureSelection() string {
	return readSelectionViaClipboard()
}

func capSelection(s string) string {
	if utf8.RuneCountInString(s) > maxSelectionChars {
		s = string([]rune(s)[:maxSelectionChars])
	}
	return s
}

// readSelectionViaClipboard mirrors the reference fallback, hardened:
// snapshot clipboard (text + sequence number), synthesize Ctrl+C, poll the
// sequence number instead of one blind sleep (a successful copy of text
// identical to the previous clipboard content is no longer misread as
// "nothing copied"), read, restore.
func readSelectionViaClipboard() string {
	before, _ := getClipboardText()
	seqBefore := getClipboardSequenceNumber()
	sendCtrlC()

	copied := false
	deadline := time.Now().Add(copyPollWindow)
	// Poll fast at first (2 ms) and back off (4, 8, 16 ms): a successful
	// copy usually lands in 1-5 ms, so the old flat 25 ms sleep added ~20 ms
	// of average latency. First check above is effectively immediate.
	delays := []time.Duration{2 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond, 16 * time.Millisecond}
	di := 0
	for {
		if getClipboardSequenceNumber() != seqBefore {
			copied = true
			break
		}
		if time.Now().After(deadline) {
			break
		}
		d := delays[di]
		if di < len(delays)-1 {
			di++
		}
		if rem := time.Until(deadline); d > rem {
			d = rem
		}
		if d > 0 {
			time.Sleep(d)
		}
	}
	if !copied {
		return ""
	}
	after, ok := getClipboardText()
	if !ok {
		return ""
	}
	s := strings.TrimSpace(after)
	if s == "" {
		return ""
	}
	// Restore what was on the clipboard before. Known cost: EmptyClipboard
	// drops non-text formats (images, file lists) on this path; leaving the
	// user's selection sitting in the clipboard was judged worse.
	if before != "" {
		_ = setClipboardText(before)
	}
	return capSelection(s)
}

// sendKey synthesizes one keyboard event through SendInput.
func sendKey(vk uint16, up bool) {
	var union [32]byte // KEYBDINPUT lives in the first 24 bytes
	binary.LittleEndian.PutUint16(union[0:], vk)
	if up {
		binary.LittleEndian.PutUint32(union[4:], keyeventfKeyup)
	}
	var input [inputSize]byte
	binary.LittleEndian.PutUint32(input[0:], inputKeyboard)
	copy(input[8:], union[:])
	procSendInput.Call(1, uintptr(unsafe.Pointer(&input[0])), inputSize)
}

// keyStroke is one synthesized keyboard event for a batched SendInput call.
type keyStroke struct {
	vk uint16
	up bool
}

// sendKeys synthesizes several keyboard events in a single SendInput call.
// Each INPUT is inputSize (40) bytes on 64-bit Windows: a type DWORD at +0,
// 4 bytes of padding, then a KEYBDINPUT starting at +8 (wVk at +8, dwFlags
// at +12). Passing the whole batch at once keeps the events adjacent in the
// input stream and avoids per-event syscall overhead.
func sendKeys(keys []keyStroke) {
	if len(keys) == 0 {
		return
	}
	buf := make([]byte, len(keys)*inputSize)
	for i, k := range keys {
		off := i * inputSize
		binary.LittleEndian.PutUint32(buf[off:], inputKeyboard)
		binary.LittleEndian.PutUint16(buf[off+8:], k.vk)
		if k.up {
			binary.LittleEndian.PutUint32(buf[off+12:], keyeventfKeyup)
		}
	}
	procSendInput.Call(uintptr(len(keys)), uintptr(unsafe.Pointer(&buf[0])), inputSize)
}

func sendCtrlC() {
	// WM_HOTKEY fires on Win+T key-down, so the user's Win is almost always
	// still physically held here. Release the modifiers first (non-negotiable:
	// otherwise the target app receives Ctrl+Win+C) as ONE SendInput batch.
	sendKeys([]keyStroke{
		{vkMenu, true},
		{vkShift, true},
		{vkLwin, true},
		{vkRwin, true},
	})
	// Let the release batch be dispatched/processed before Ctrl+C arrives: a
	// slow foreground app can otherwise still observe Win as held and turn the
	// copy into Ctrl+Win+C. ~20 ms is enough even on a loaded machine; the old
	// 4x30 ms was pure waste. Two batches (not one) because the release must be
	// observed as completed modifier state before Ctrl+C is injected.
	time.Sleep(20 * time.Millisecond)
	// Ctrl+C as one atomic batch: down, down, up, up in input order.
	sendKeys([]keyStroke{
		{vkControl, false},
		{vkC, false},
		{vkC, true},
		{vkControl, true},
	})
}
