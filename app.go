package main

import (
	"context"
	"fmt"
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
}

// Greet returns a greeting for the given name
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, It's show time!", name)
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
