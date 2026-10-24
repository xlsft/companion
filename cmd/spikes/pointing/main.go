//go:build linux

// Command pointing drives the real overlay pipeline end to end without a
// model: display tracker → overlay engine → platform overlay backend. It
// simulates a point_at tool call on a synthetic capture of the primary
// display, keeps it on screen, then clears it (OC-49, OC-50, OC-51).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/platform/detect"
	pdisplays "github.com/xlsft/companion/internal/platform/displays"
	wloverlay "github.com/xlsft/companion/internal/platform/linuxwayland/overlay"
	"github.com/xlsft/companion/pkg/contracts"
)

func main() {
	x := flag.Float64("x", 0.5, "normalized x on the primary display")
	y := flag.Float64("y", 0.5, "normalized y on the primary display")
	label := flag.String("label", "Here", "label next to the ring")
	hold := flag.Duration("hold", 3*time.Second, "how long to show the hint")
	flag.Parse()
	if err := run(*x, *y, *label, *hold); err != nil {
		fmt.Fprintln(os.Stderr, "pointing:", err)
		os.Exit(1)
	}
}

func run(x, y float64, label string, hold time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), hold+10*time.Second)
	defer cancel()

	report, err := detect.New().Detect(ctx)
	if err != nil {
		return err
	}
	tracker, err := pdisplays.New(ctx, report)
	if err != nil {
		return err
	}
	defer tracker.Close()
	topo, err := tracker.Topology(ctx)
	if err != nil {
		return err
	}
	var primary contracts.Display
	for _, d := range topo.Displays {
		if d.Primary || primary.ID == "" {
			primary = d
		}
	}
	host, err := wloverlay.New(ctx)
	if err != nil {
		return err
	}
	defer host.Close()

	engine := overlay.NewEngine(host, tracker.Topology)
	// A capture covering the whole primary display, as the screen capture
	// backend would produce it.
	meta := contracts.CaptureMetadata{
		ID: "spike", DisplayID: primary.ID, Native: primary.Native, Bounds: primary.Bounds,
		Scale: primary.Scale, Rotation: primary.Rotation, TopologyVersion: topo.Version, Timestamp: time.Now(),
	}
	engine.RegisterCapture(meta)

	ref := contracts.SessionRef{ID: "spike", Generation: 1}
	args, _ := json.Marshal(map[string]any{"x": x, "y": y, "label": label})
	cmds, err := overlay.FromToolCall(overlay.CallContext{Session: ref, Capture: meta.ID, CallID: "c1", Segment: -1},
		overlay.ToolPointAt, args)
	if err != nil {
		return err
	}
	start := time.Now()
	for _, c := range cmds {
		if err := engine.Apply(ctx, c); err != nil {
			return err
		}
	}
	fmt.Printf("display=%s topology=v%d shown in %s\n", primary.ID, topo.Version, time.Since(start))
	time.Sleep(hold)
	start = time.Now()
	engine.Clear(ctx)
	fmt.Printf("cleared in %s\n", time.Since(start))
	return nil
}
