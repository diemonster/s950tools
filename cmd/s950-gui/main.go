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

	// Create application with options. Window defaults match the
	// mockup target (1400px viewport + a little chrome).
	err := wails.Run(&options.App{
		Title:     "s950-tools",
		Width:     1440,
		Height:    900,
		MinWidth:  1200,
		MinHeight: 720,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// Light app background matches the mockups' --grey-light.
		BackgroundColour: &options.RGBA{R: 234, G: 234, B: 234, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
