package main

import (
	"context"
	"time"
	"unsafe"
)

// App struct
type App struct {
	ctx context.Context
}

// TranslationResult is returned to the frontend by TranslateText.
type TranslationResult struct {
	TranslatedText string `json:"translatedText"`
	DetectedSource string `json:"detectedSource"`
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.startHotkeyLoop()
	a.startKbdHook()
	a.applyRoundedCorners()
}

// applyRoundedCorners asks DWM (Windows 11) to round the frameless window's
// corners; on Windows 10 the call fails and the window stays square.
func (a *App) applyRoundedCorners() {
	go func() {
		// The window handle may not exist yet at startup; retry briefly.
		for i := 0; i < 20; i++ {
			if hwnd := findTranslatorWindow(); hwnd != 0 {
				pref := uint32(dwmwcpRound)
				procDwmSetWindowAttribute.Call(
					hwnd, dwmwaWindowCornerPreference, uintptr(unsafe.Pointer(&pref)), 4)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

// TranslateText translates text via the keyless Google endpoint.
// Errors reject the JS promise with an English message for the output pane.
func (a *App) TranslateText(text, source, target string) (TranslationResult, error) {
	return translateText(text, source, target)
}

// CopyText writes text to the system clipboard (raw Win32, no-op on empty).
func (a *App) CopyText(t string) {
	if t == "" {
		return
	}
	_ = setClipboardText(t)
}
