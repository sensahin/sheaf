// Command sheaf-desktop is the Sheaf desktop app: a native window on the
// system webview (WebKit on macOS and Linux, WebView2 on Windows) showing your
// queue, agents, tasks and messages, with desktop notifications when an
// agent needs you.
//
// It starts the daemon if it is not running and pairs itself as the device
// "desktop". The page is served inside the app, with no network port: the
// webview's requests go straight to a handler that serves the web queue and
// forwards /v1 to the daemon's socket. Closing the window leaves the daemon
// running, because your agents still use it.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/config"
	"github.com/sensahin/sheaf/internal/desktop"
)

//go:embed build/appicon.png
var icon []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sheaf-desktop:", err)
		os.Exit(1)
	}
}

// App is bound into the page as window.go.main.App.
type App struct {
	ctx   context.Context
	token string
}

// Token returns the app's device token, so the page needs no pairing link.
func (a *App) Token() string { return a.token }

// SetTitle shows the page's title, which carries the queue count, on the
// window and its dock or taskbar entry.
func (a *App) SetTitle(title string) {
	if a.ctx != nil {
		wruntime.WindowSetTitle(a.ctx, title)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	paths, err := config.Resolve()
	if err != nil {
		return err
	}
	if !desktop.Running(paths.Socket) {
		bin, err := desktop.FindSheaf()
		if err != nil {
			return err
		}
		if _, err := desktop.EnsureDaemon(ctx, paths, bin); err != nil {
			return err
		}
	}
	token, err := desktop.DeviceToken(ctx, paths)
	if err != nil {
		return err
	}
	go desktop.Watch(ctx, api.NewClient(paths.Socket, token), desktop.Notify)

	app := &App{token: token}
	return wails.Run(&options.App{
		Title:     "Sheaf",
		Width:     1180,
		Height:    800,
		MinWidth:  420,
		MinHeight: 480,
		// The page is served by the app itself, so no other process or web
		// page can reach this handler; every API call still carries the
		// page's own device token.
		AssetServer: &assetserver.Options{
			Handler: desktop.Handler(paths.Socket),
		},
		BackgroundColour: &options.RGBA{R: 0x15, G: 0x20, B: 0x1B, A: 0xff},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "dev.sheaf.desktop",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				if app.ctx != nil {
					wruntime.WindowUnminimise(app.ctx)
					wruntime.WindowShow(app.ctx)
				}
			},
		},
		OnStartup: func(c context.Context) { app.ctx = c },
		Bind:      []any{app},
		Linux:     &linux.Options{Icon: icon, ProgramName: "sheaf-desktop"},
		Mac: &mac.Options{
			About: &mac.AboutInfo{Title: "Sheaf", Message: "Coordination for your coding agents."},
		},
	})
}
