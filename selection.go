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
	for {
		if getClipboardSequenceNumber() != seqBefore {
			copied = true
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
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

func sendCtrlC() {
	// WM_HOTKEY fires on Alt+T key-down, so the user's Alt is almost always
	// still physically held here. Release the synthesized-key-state modifiers
	// first, or the target app receives Ctrl+Alt+C (no copy) instead of Ctrl+C.
	sendKey(vkMenu, true)
	sendKey(vkShift, true)
	sendKey(vkLwin, true)
	sendKey(vkRwin, true)
	time.Sleep(30 * time.Millisecond)
	sendKey(vkControl, false)
	time.Sleep(30 * time.Millisecond)
	sendKey(vkC, false)
	time.Sleep(30 * time.Millisecond)
	sendKey(vkC, true)
	time.Sleep(30 * time.Millisecond)
	sendKey(vkControl, true)
}
