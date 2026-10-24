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
	"syscall"
	"time"

	"github.com/xlsft/openclick/internal/app"
	"github.com/xlsft/openclick/internal/buildinfo"
	"github.com/xlsft/openclick/internal/platform/detect"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	diagnose := flag.Bool("diagnose", false, "print the platform capability report as JSON and exit")
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
	defer stop()

	if err := app.New(log).Run(ctx); err != nil {
		log.Error("companion exited with error", "err", err)
		os.Exit(1)
	}
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
