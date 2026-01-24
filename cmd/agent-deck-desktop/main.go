package main

import (
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Detect development mode
	isDev := os.Getenv("WAILS_DEV") != "" || Version == "0.1.0-dev"

	// Configure logger
	logLevel := logger.INFO
	if isDev {
		logLevel = logger.DEBUG
	}

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "agent-deck-desktop",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		LogLevel:           logLevel,
		LogLevelProduction: logger.ERROR,
		// Enable DevTools in development mode
		Debug: options.Debug{
			OpenInspectorOnStartup: isDev,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
