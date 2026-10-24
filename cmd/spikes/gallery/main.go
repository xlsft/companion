//go:build linux

// Command gallery shows every overlay hint kind in turn on the primary
// display through the real pipeline (display tracker → overlay engine →
// platform backend), each as the model would request it, so their look
// and animation can be reviewed on a live desktop (OC-50).
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

type scene struct {
	name string
	tool string
	args string
}

var scenes = []scene{
	{"point_at", overlay.ToolPointAt, `{"x":0.30,"y":0.40,"label":"Метроном"}`},
	{"ring", overlay.ToolAnnotate, `{"shapes":[{"kind":"ring","points":[[0.30,0.40]]}]}`},
	{"circle", overlay.ToolAnnotate, `{"shapes":[{"kind":"circle","points":[[0.30,0.40]],"radius":0.025}]}`},
	{"circle dashed", overlay.ToolAnnotate, `{"shapes":[
		{"kind":"circle","points":[[0.26,0.40]],"radius":0.012,"dashed":true},
		{"kind":"circle","points":[[0.30,0.40]],"radius":0.012,"dashed":true},
		{"kind":"circle","points":[[0.34,0.40]],"radius":0.012,"dashed":true},
		{"kind":"circle","points":[[0.38,0.40]],"radius":0.012,"dashed":true}]}`},
	{"rect", overlay.ToolAnnotate, `{"shapes":[{"kind":"rect","points":[[0.20,0.33],[0.45,0.47]]}]}`},
	{"brackets", overlay.ToolAnnotate, `{"shapes":[{"kind":"brackets","points":[[0.20,0.33],[0.45,0.47]]}]}`},
	{"highlight", overlay.ToolAnnotate, `{"shapes":[{"kind":"highlight","points":[[0.20,0.38],[0.45,0.42]]}]}`},
	{"underline", overlay.ToolAnnotate, `{"shapes":[{"kind":"underline","points":[[0.20,0.43],[0.45,0.43]]}]}`},
	{"arrow", overlay.ToolAnnotate, `{"shapes":[{"kind":"arrow","points":[[0.50,0.60],[0.32,0.42]]}]}`},
	{"freehand", overlay.ToolAnnotate, `{"shapes":[{"kind":"freehand","points":[[0.20,0.50],[0.24,0.44],[0.28,0.52],[0.32,0.45],[0.36,0.53],[0.40,0.46]]}]}`},
	{"label", overlay.ToolAnnotate, `{"shapes":[{"kind":"label","points":[[0.25,0.40]],"text":"Кнопка экспорта"}]}`},
	{"badge", overlay.ToolAnnotate, `{"shapes":[
		{"kind":"badge","points":[[0.24,0.40]],"text":"1"},
		{"kind":"badge","points":[[0.30,0.40]],"text":"2"},
		{"kind":"badge","points":[[0.36,0.40]],"text":"3"}]}`},
	{"spotlight", overlay.ToolAnnotate, `{"shapes":[{"kind":"spotlight","points":[[0.30,0.40]]}]}`},
	{"вместе", overlay.ToolAnnotate, `{"shapes":[
		{"kind":"brackets","points":[[0.18,0.30],[0.47,0.50]]},
		{"kind":"badge","points":[[0.18,0.30]],"text":"1"},
		{"kind":"highlight","points":[[0.21,0.37],[0.44,0.41]]},
		{"kind":"arrow","points":[[0.62,0.62],[0.47,0.50]]},
		{"kind":"label","points":[[0.62,0.64]],"text":"Сначала выделите дорожку"}]}`},
}

func main() {
	hold := flag.Duration("hold", 3*time.Second, "how long to show each scene")
	only := flag.String("only", "", "show just the scene with this name")
	flag.Parse()
	if *only != "" {
		var keep []scene
		for _, sc := range scenes {
			if sc.name == *only {
				keep = append(keep, sc)
			}
		}
		if len(keep) == 0 {
			fmt.Fprintln(os.Stderr, "gallery: no scene", *only)
			os.Exit(2)
		}
		scenes = keep
	}
	if err := run(*hold); err != nil {
		fmt.Fprintln(os.Stderr, "gallery:", err)
		os.Exit(1)
	}
}

func run(hold time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(scenes))*hold+20*time.Second)
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
	meta := contracts.CaptureMetadata{
		ID: "gallery", DisplayID: primary.ID, Native: primary.Native, Bounds: primary.Bounds,
		Scale: primary.Scale, Rotation: primary.Rotation, TopologyVersion: topo.Version, Timestamp: time.Now(),
	}
	defer engine.Clear(context.Background())

	for i, sc := range scenes {
		engine.Clear(ctx)
		engine.RegisterCapture(meta)
		// Each scene is a new session generation, as successive requests are.
		cc := overlay.CallContext{Session: contracts.SessionRef{ID: "gallery", Generation: uint64(i + 1)}, Capture: meta.ID, CallID: sc.name, Segment: -1}
		cmds, err := overlay.FromToolCall(cc, sc.tool, json.RawMessage(sc.args))
		if err != nil {
			return fmt.Errorf("%s: %w", sc.name, err)
		}
		caption, _ := json.Marshal(map[string]any{"shapes": []map[string]any{
			{"kind": "label", "points": [][]float64{{0.02, 0.06}}, "text": fmt.Sprintf("%d/%d · %s", i+1, len(scenes), sc.name)},
		}})
		capCmds, err := overlay.FromToolCall(cc, overlay.ToolAnnotate, caption)
		if err != nil {
			return err
		}
		for i := range capCmds {
			capCmds[i].ID = "caption"
		}
		for _, c := range append(cmds, capCmds...) {
			if err := engine.Apply(ctx, c); err != nil {
				return fmt.Errorf("%s: %w", sc.name, err)
			}
		}
		fmt.Printf("%d/%d %s\n", i+1, len(scenes), sc.name)
		select {
		case <-time.After(hold):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
