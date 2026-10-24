// Command companion is the core desktop process: UI, orchestration and
// platform bridges.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	gioapp "gioui.org/app"

	"github.com/xlsft/companion/internal/app"
	"github.com/xlsft/companion/internal/buildinfo"
	"github.com/xlsft/companion/internal/platform/appearance"
	"github.com/xlsft/companion/internal/platform/control"
	"github.com/xlsft/companion/internal/platform/desktop"
	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/internal/platform/tray"
	"github.com/xlsft/companion/internal/secrets"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/internal/ui"
	"github.com/xlsft/companion/pkg/contracts"
)

func main() {
	// "companion ctl ..." is run by compositor keybindings once per key
	// press, so it must stay a quick client and never start the UI.
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		os.Exit(runCtl(os.Args[2:]))
	}
	showVersion := flag.Bool("version", false, "print version and exit")
	diagnose := flag.Bool("diagnose", false, "print the platform capability report as JSON and exit")
	dataDir := flag.String("data", "", "data directory (default: user config dir/openclick)")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.String())
		return
	}
	if *diagnose {
		runDiagnose()
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		log.Error("data directory", "err", err)
		os.Exit(1)
	}
	db, svc, err := openSettings(ctx, dir)
	if err != nil {
		log.Error("open settings", "err", err)
		os.Exit(1)
	}
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	report, err := detect.New().Detect(dctx)
	dcancel()
	if err != nil {
		log.Warn("capability detection", "err", err)
	}
	c, err := newCore(ctx, log, dir, db, svc, report, secrets.NewKeyring())
	if err != nil {
		log.Error("start core", "err", err)
		db.Close()
		os.Exit(1)
	}
	gate, err := openGate(ctx, store.NewKV(db), c.setupChecks())
	if err != nil {
		log.Error("open readiness gate", "err", err)
		db.Close()
		os.Exit(1)
	}
	c.gate = gate
	// Only a Ready gate gets the main UI; there is no flag around setup
	// (FR-ONB-004).
	mode := ui.ModeSetup
	if gate.IsReady() {
		mode = ui.ModeMain
	}
	var tr contracts.Tray
	if svc.Get().Platform.TrayIcon {
		tr = tray.New()
	}
	// An installed update restarts the process: the lifecycle stops
	// cleanly first, then the new binary replaces this one (below).
	var restart atomic.Bool
	exe, exeErr := os.Executable()
	var onInstalled func()
	if exeErr == nil {
		onInstalled = func() {
			restart.Store(true)
			stop()
		}
	}
	upd := newUpdater(log, dir, svc, exe, onInstalled)
	// The theme follows the desktop; there is deliberately no flag for it.
	scheme := appearance.New()
	if err := scheme.Start(ctx); err != nil {
		log.Warn("system colour scheme unavailable, using light", "err", err)
	}
	shell := ui.New(ui.Options{
		Mode:       mode,
		Settings:   svc,
		Gate:       gate,
		Setup:      c.setupBackends(),
		Chat:       c.chat,
		Guide:      c.guide,
		Tray:       tr,
		Diagnose:   detect.New().Detect,
		Updates:    upd,
		Memory:     c.memory,
		Quit:       stop,
		Log:        log,
		Appearance: scheme,
		Version:    buildinfo.Version,
	})

	// Gio must own the main goroutine (macOS requires the main thread), so
	// the lifecycle runs beside it and ends the process when it returns.
	go func() {
		code := 0
		if err := app.New(log, c, upd, shell).Run(ctx); err != nil {
			log.Error("companion exited with error", "err", err)
			code = 1
		}
		stop()
		if err := db.Close(); err != nil {
			log.Error("close store", "err", err)
		}
		if restart.Load() {
			// The updater replaced the binaries in place, so the same path
			// now holds the new version.
			err := desktop.Reexec(exe, os.Args[1:])
			log.Error("restart after update failed; start Companion again", "err", err)
			os.Exit(1)
		}
		os.Exit(code)
	}()
	gioapp.Main()
}

// resolveDataDir is dir, or the user config dir's openclick folder.
func resolveDataDir(dir string) (string, error) {
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "openclick")
	}
	return dir, os.MkdirAll(dir, 0o700)
}

func openSettings(ctx context.Context, dir string) (*store.DB, *settings.Service, error) {
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

// runCtl sends one command to the running core's control socket.
func runCtl(args []string) int {
	fs := flag.NewFlagSet("ctl", flag.ContinueOnError)
	socket := fs.String("socket", "", "control socket path (default $XDG_RUNTIME_DIR/openclick/control.sock)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: companion ctl [-socket path] <%s>\n", strings.Join(control.CtlCommands(), "|"))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path := *socket
	if path == "" {
		p, err := control.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "companion ctl:", err)
			return 1
		}
		path = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := control.Ctl(ctx, path, fs.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "companion ctl:", err)
		if errors.Is(err, control.ErrUnknownCtl) {
			fs.Usage()
			return 2
		}
		return 1
	}
	return 0
}
