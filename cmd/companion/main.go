// Command companion is the core desktop process: UI, orchestration and
// platform bridges.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/xlsft/openclick/internal/app"
	"github.com/xlsft/openclick/internal/buildinfo"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.String())
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
