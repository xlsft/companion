//go:build live && unix

package main

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/platform/capture"
	"github.com/xlsft/companion/internal/platform/detect"
	pdisplays "github.com/xlsft/companion/internal/platform/displays"
	"github.com/xlsft/companion/internal/walkthrough"
	"github.com/xlsft/companion/pkg/contracts"
)

// stillPresenter shows a step at a fixed area and never asks the model.
type stillPresenter struct{ area contracts.Rect }

func (p stillPresenter) Show(_ context.Context, pr walkthrough.Presentation) (walkthrough.Shown, error) {
	return walkthrough.Shown{Step: pr.Step, Area: p.area}, nil
}
func (stillPresenter) Check(context.Context, walkthrough.Presentation) (walkthrough.Verdict, error) {
	return walkthrough.Verdict{}, nil
}
func (stillPresenter) Say(context.Context, string) error         { return nil }
func (stillPresenter) End(context.Context, contracts.SessionRef) {}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// TestLiveWatchCost measures what watching a walkthrough step costs on
// this machine, for a step marked on screen and for one without hints
// (whole display), against the process idling (OC-68):
//
//	go test -tags=novulkan,live -run TestLiveWatchCost -v ./cmd/companion
func TestLiveWatchCost(t *testing.T) {
	const run = 12 * time.Second
	ctx := context.Background()
	report, err := detect.New().Detect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	displays, err := pdisplays.New(ctx, report)
	if err != nil {
		t.Skipf("no displays: %v", err)
	}
	capt, err := capture.New(ctx, report, displays)
	if err != nil {
		t.Skipf("no capture: %v", err)
	}
	defer capt.Close()
	topo, err := displays.Topology(ctx)
	if err != nil || len(topo.Displays) == 0 {
		t.Skipf("no topology: %v", err)
	}
	b := topo.Displays[0].Bounds
	ring := contracts.RectXYWH(b.Min.X+b.Width()*0.4, b.Min.Y+b.Height()*0.4, b.Width()*0.06, b.Width()*0.06)
	measure := func(name string, area *contracts.Rect) {
		var counted *countingCapturer
		var s *walkthrough.Service
		if area != nil {
			counted = &countingCapturer{ScreenCapturer: capt}
			s = walkthrough.New(walkthrough.Config{Presenter: stillPresenter{*area}, Watch: &walkthrough.Watch{Capturer: counted}})
			if err := s.Start(ctx, walkthrough.Plan{Goal: "g", Steps: []walkthrough.Step{{Title: "t", Say: "s"}}}, ""); err != nil {
				t.Fatal(err)
			}
		}
		c0, t0 := cpuTime(), time.Now()
		time.Sleep(run)
		cpu, wall := cpuTime()-c0, time.Since(t0)
		samples := 0
		if s != nil {
			s.Close()
			samples = counted.n
		}
		t.Logf("%-22s cpu %6v over %v = %.2f%% of one core, %d samples", name, cpu.Round(time.Millisecond), wall.Round(time.Second),
			100*cpu.Seconds()/wall.Seconds(), samples)
	}
	t.Logf("backend %s, display %s %vx%v", capt.Backend(), topo.Displays[0].ID, b.Width(), b.Height())
	measure("idle", nil)
	measure("step with hints", &ring)
	measure("step without hints", &contracts.Rect{})
}

type countingCapturer struct {
	contracts.ScreenCapturer
	n int
}

func (c *countingCapturer) Capture(ctx context.Context, req contracts.CaptureRequest) ([]*contracts.Capture, error) {
	c.n++
	return c.ScreenCapturer.Capture(ctx, req)
}
