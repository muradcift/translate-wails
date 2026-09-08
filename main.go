package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:         "Translator",
		Width:         760,
		Height:        500,
		DisableResize: true,
		Frameless:     true,
		StartHidden:   true,
		SingleInstanceLock: &options.SingleInstanceLock{
			// Empty UniqueId would share a machine-global "wails-app-" namespace
			// with any other Wails app that does the same.
			UniqueId: "translate-wails-muradcift",
		},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 20, G: 20, B: 25, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
