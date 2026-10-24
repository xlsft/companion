// Command displays prints the display topology as JSON and then every
// change, so a monitor can be plugged or unplugged while it runs. With
// -session it overrides detection (wayland or x11), e.g. to try Xwayland.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/xlsft/companion/internal/platform/detect"
	platdisplays "github.com/xlsft/companion/internal/platform/displays"
	"github.com/xlsft/companion/pkg/contracts"
)

func main() {
	session := flag.String("session", "", "override session kind: wayland or x11")
	flag.Parse()
	if err := run(*session); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(override string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	report, err := detect.New().Detect(ctx)
	if err != nil {
		return err
	}
	if override != "" {
		report.Session = contracts.SessionKind(override)
	}
	fmt.Fprintf(os.Stderr, "session=%s compositor=%s\n", report.Session, report.Compositor)

	tr, err := platdisplays.New(ctx, report)
	if err != nil {
		return err
	}
	defer tr.Close()

	w, err := tr.Watch(ctx)
	if err != nil {
		return err
	}
	topo, err := tr.Topology(ctx)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(topo); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "watching for changes, Ctrl-C to stop")
	for t := range w {
		if err := enc.Encode(t); err != nil {
			return err
		}
	}
	return nil
}
