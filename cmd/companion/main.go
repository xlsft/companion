// Command companion is the core desktop process: UI, orchestration and
// platform bridges.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	gioapp "gioui.org/app"

	"github.com/xlsft/openclick/internal/app"
	"github.com/xlsft/openclick/internal/buildinfo"
	"github.com/xlsft/openclick/internal/platform/detect"
	"github.com/xlsft/openclick/internal/platform/tray"
	"github.com/xlsft/openclick/internal/settings"
	"github.com/xlsft/openclick/internal/store"
	"github.com/xlsft/openclick/internal/ui"
	"github.com/xlsft/openclick/pkg/contracts"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	diagnose := flag.Bool("diagnose", false, "print the platform capability report as JSON and exit")
	// Until onboarding (OC-70) decides readiness, the mode is chosen by hand.
	modeFlag := flag.String("mode", "setup", "debug: UI mode, setup or main")
	dataDir := flag.String("data", "", "data directory (default: user config dir/openclick)")
	dark := flag.Bool("dark", false, "use the dark theme")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.String())
		return
	}
	if *diagnose {
		runDiagnose()
		return
	}
	mode, err := ui.ParseMode(*modeFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	db, svc, err := openSettings(ctx, *dataDir)
	if err != nil {
		log.Error("open settings", "err", err)
		os.Exit(1)
	}
	var tr contracts.Tray
	if svc.Get().Platform.TrayIcon {
		tr = tray.New()
	}
	shell := ui.New(ui.Options{
		Mode:     mode,
		Settings: svc,
		Tray:     tr,
		Diagnose: detect.New().Detect,
		Quit:     stop,
		Log:      log,
		Dark:     *dark,
		Version:  buildinfo.String(),
	})

	// Gio must own the main goroutine (macOS requires the main thread), so
	// the lifecycle runs beside it and ends the process when it returns.
	go func() {
		code := 0
		if err := app.New(log, shell).Run(ctx); err != nil {
			log.Error("companion exited with error", "err", err)
			code = 1
		}
		stop()
		if err := db.Close(); err != nil {
			log.Error("close store", "err", err)
		}
		os.Exit(code)
	}()
	gioapp.Main()
}

func openSettings(ctx context.Context, dir string) (*store.DB, *settings.Service, error) {
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, nil, err
		}
		dir = filepath.Join(base, "openclick")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	db, err := store.Open(ctx, filepath.Join(dir, "companion.db"))
	if err != nil {
		return nil, nil, err
	}
	svc, err := settings.NewService(ctx, store.NewKV(db))
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, svc, nil
}

// runDiagnose prints the capability report. It contains technical facts only,
// no user content, so the output is safe to paste into bug reports.
func runDiagnose() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := detect.New().Detect(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "diagnose:", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "diagnose:", err)
		os.Exit(1)
	}
}
