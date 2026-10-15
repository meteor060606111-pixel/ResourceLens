package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"resourcelens/frontend"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	winoptions "github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var assets = frontend.Assets()

type App struct {
	engine *Engine
	ctx    context.Context
}

func (a *App) Bootstrap() map[string]any { return a.engine.bootstrap() }
func (a *App) ChooseDirectory() (string, error) {
	return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{Title: "选择下载目录", DefaultDirectory: a.engine.configSnapshot().DownloadDir})
}
func (a *App) CopyText(s string) error { return wruntime.ClipboardSetText(a.ctx, s) }
func (a *App) ChooseUpdateFile() (string, error) {
	ext := updatePackageExtension()
	return wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{Title: "选择拾流新版安装包", Filters: []wruntime.FileFilter{{DisplayName: "拾流安装包 (*" + ext + ")", Pattern: "*" + ext}}})
}

func main() {
	headless := flag.Bool("headless", false, "serve the same interface in a browser")
	mobile := flag.Bool("mobile", false, "enable the paired phone interface for this run")
	root := flag.String("root", "", "application data directory")
	apiPort := flag.Int("api-port", 0, "local UI port; 0 selects a free port")
	updateTask := flag.String("update-worker", "", "internal update task")
	flag.Parse()
	if *updateTask != "" {
		if err := runUpdateWorker(*updateTask); err != nil {
			log.Printf("升级助手失败：%v", err)
		}
		return
	}
	var instance *desktopInstance
	if !*headless {
		var alreadyRunning bool
		var err error
		instance, alreadyRunning, err = acquireDesktopInstance()
		if err != nil {
			startupError(fmt.Errorf("单实例启动失败：%w", err))
			return
		}
		if alreadyRunning {
			return
		}
		defer instance.Close()
	}
	if *root == "" {
		var err error
		*root, err = defaultApplicationRoot()
		if err != nil {
			if !*headless {
				startupError(err)
			}
			return
		}
	}
	if err := os.MkdirAll(filepath.Join(*root, "data"), 0700); err != nil {
		log.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(*root, "data", "app.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err == nil {
		defer logFile.Close()
		log.SetOutput(logFile)
	}
	engine, err := NewEngine(*root, assets, *apiPort)
	if err != nil {
		log.Print(err)
		if !*headless {
			startupError(err)
		}
		return
	}
	defer engine.Close()
	engine.loadUpdateInfo()
	if *mobile {
		if err := engine.startMobile(); err != nil {
			log.Print(err)
			if !*headless {
				startupError(err)
			}
			return
		}
	}
	if *headless {
		fmt.Println(engine.baseURL)
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		return
	}
	app := &App{engine: engine}
	background := &options.RGBA{R: 233, G: 240, B: 249, A: 255}
	var macOptions *mac.Options
	var windowsOptions *winoptions.Options
	switch runtime.GOOS {
	case "windows":
		background.A = 0
		windowsOptions = &winoptions.Options{WebviewIsTransparent: true, WindowIsTranslucent: true, BackdropType: winoptions.Acrylic, Theme: winoptions.Light}
	case "darwin":
		background = &options.RGBA{R: 255, G: 255, B: 255, A: 0}
		macOptions = &mac.Options{TitleBar: mac.TitleBarHiddenInset(), Appearance: mac.NSAppearanceNameAqua, WebviewIsTransparent: true, WindowIsTranslucent: true}
	}
	err = wails.Run(&options.App{Title: "拾流 · ResourceLens", Width: 1280, Height: 800, MinWidth: 980, MinHeight: 650, BackgroundColour: background, Windows: windowsOptions, Mac: macOptions, AssetServer: &assetserver.Options{Assets: assets}, Bind: []interface{}{app}, OnStartup: func(ctx context.Context) {
		app.ctx = ctx
		quitMu.Lock()
		requestApplicationQuit = func() { engine.Close(); wruntime.Quit(ctx) }
		quitMu.Unlock()
	}, OnDomReady: func(ctx context.Context) {
		instance.listen(func() { wruntime.WindowShow(ctx); wruntime.WindowUnminimise(ctx); activateCurrentWindow() })
	}, OnShutdown: func(context.Context) { quitMu.Lock(); requestApplicationQuit = nil; quitMu.Unlock() }, OnBeforeClose: func(context.Context) bool { engine.Close(); return false }})
	if err != nil {
		log.Println(err)
		startupError(err)
	}
}
