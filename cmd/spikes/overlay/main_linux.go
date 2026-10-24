//go:build linux

// Command overlay is the OC-5 spike: it shows a click-through test frame on
// every output for N seconds, briefly makes the label card interactive, and
// reports focus changes, pointer events and frame timings.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"time"

	wloverlay "github.com/xlsft/companion/internal/platform/linuxwayland/overlay"
	x11overlay "github.com/xlsft/companion/internal/platform/linuxx11/overlay"
	"github.com/xlsft/companion/pkg/contracts"
)

type host interface {
	contracts.OverlayHost
	Displays() []contracts.Display
	Close() error
}

type surface interface {
	contracts.OverlaySurface
	NativeSize() contracts.PixelSize
	Scale() float64
	LastFrameLatency() time.Duration
}

func main() {
	seconds := flag.Int("seconds", 6, "how long to show the overlay")
	backend := flag.String("backend", "auto", "auto | wayland | x11")
	bench := flag.Int("bench", 30, "frames to present for the timing benchmark (0 = skip)")
	list := flag.Bool("list", false, "list Wayland globals and exit")
	selftest := flag.Bool("selftest", false, "Wayland: verify click-through with a virtual pointer (moves the cursor, clicks only on the spike's own surfaces)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *backend, time.Duration(*seconds)*time.Second, *bench, *list, *selftest); err != nil {
		fmt.Fprintln(os.Stderr, "overlay spike:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, backend string, dur time.Duration, bench int, list, selftest bool) error {
	if backend == "auto" {
		backend = "x11"
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			backend = "wayland"
		}
	}
	focusBefore := focused(backend)
	fmt.Printf("backend=%s focused-before=%q\n", backend, focusBefore)

	var h host
	var ptr <-chan wloverlay.PointerEvent
	var wh *wloverlay.Host
	switch backend {
	case "wayland":
		var err error
		wh, err = wloverlay.New(ctx)
		if err != nil {
			return err
		}
		if list {
			g := wh.Globals()
			names := make([]string, 0, len(g))
			for n := range g {
				names = append(names, n)
			}
			slices.Sort(names)
			for _, n := range names {
				fmt.Printf("%s v%d\n", n, g[n])
			}
			return wh.Close()
		}
		fmt.Printf("fractional-scale+viewporter=%v\n", wh.HasFractionalScale())
		ptr = wh.PointerEvents()
		h = wh
	case "x11":
		xh, err := x11overlay.New(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("x11 composited=%v\n", xh.Composited())
		h = xh
	default:
		return fmt.Errorf("unknown backend %q", backend)
	}
	defer h.Close()

	displays := h.Displays()
	if len(displays) == 0 {
		return errors.New("no displays")
	}
	var surfaces []surface
	var cards []contracts.Rect
	for _, d := range displays {
		t0 := time.Now()
		s, err := h.Open(ctx, d.ID)
		if err != nil {
			return fmt.Errorf("open %s: %w", d.ID, err)
		}
		ss := s.(surface)
		openDur := time.Since(t0)
		n := ss.NativeSize()
		sc := ss.Scale()
		t1 := time.Now()
		frame, card := testFrame(n.W, n.H, sc, float64(n.W)/2, float64(n.H)/2, "openclick OC-5 "+string(d.ID))
		renderDur := time.Since(t1)
		t2 := time.Now()
		if err := ss.Present(ctx, frame); err != nil {
			return fmt.Errorf("present %s: %w", d.ID, err)
		}
		fmt.Printf("display=%s native=%dx%d scale=%.3f open=%v render=%v present-call=%v\n",
			d.ID, n.W, n.H, sc, openDur.Round(time.Microsecond), renderDur.Round(time.Microsecond),
			time.Since(t2).Round(time.Microsecond))
		surfaces = append(surfaces, ss)
		cards = append(cards, contracts.Rect{
			Min: contracts.Point{X: float64(card.Min.X) / sc, Y: float64(card.Min.Y) / sc},
			Max: contracts.Point{X: float64(card.Max.X) / sc, Y: float64(card.Max.Y) / sc},
		})
	}
	time.Sleep(100 * time.Millisecond)
	for _, s := range surfaces {
		fmt.Printf("display=%s first-frame-callback=%v\n", s.Display(), s.LastFrameLatency().Round(time.Microsecond))
	}
	if bench > 0 {
		benchmark(ctx, surfaces[0], bench)
	}
	fmt.Printf("focused-after-map=%q stolen=%v\n", focused(backend), focused(backend) != focusBefore)

	if selftest && wh != nil {
		rep, err := wh.SelfTestClickThrough(ctx, surfaces[0], cards[0])
		for _, e := range rep.Events {
			fmt.Printf("  selftest %s %-15s %-6s btn=%#x pressed=%v at=%.0f,%.0f\n",
				e.Time.Format("15:04:05.000"), e.Target, e.Kind, e.Button, e.Pressed, e.X, e.Y)
		}
		if err != nil {
			fmt.Println("selftest error:", err)
		}
		fmt.Printf("selftest pass-through=%v interactive-region=%v restored=%v\n", rep.PassThrough, rep.Interactive, rep.Restored)
		fmt.Printf("focused-after-selftest=%q stolen=%v\n", focused(backend), focused(backend) != focusBefore)
	}

	go func() {
		for pe := range ptr {
			fmt.Printf("%s pointer-%s target=%s display=%s at=%.0f,%.0f\n",
				pe.Time.Format("15:04:05.000"), pe.Kind, pe.Target, pe.Display, pe.X, pe.Y)
		}
	}()

	third := dur / 3
	fmt.Printf("phase 1: fully click-through for %v (pointer events here = bug)\n", third)
	if !sleep(ctx, third) {
		return ctx.Err()
	}
	for i, s := range surfaces {
		if err := s.SetInteractive(cards[i]); err != nil {
			return err
		}
		fmt.Printf("phase 2: display=%s interactive=%.0f,%.0f-%.0f,%.0f for %v (hover the label card: expect pointer-enter)\n",
			s.Display(), cards[i].Min.X, cards[i].Min.Y, cards[i].Max.X, cards[i].Max.Y, third)
	}
	if !sleep(ctx, third) {
		return ctx.Err()
	}
	for _, s := range surfaces {
		if err := s.SetInteractive(contracts.Rect{}); err != nil {
			return err
		}
	}
	fmt.Printf("phase 3: click-through restored for %v\n", dur-2*third)
	if !sleep(ctx, dur-2*third) {
		return ctx.Err()
	}
	after := focused(backend)
	fmt.Printf("focused-after=%q stolen=%v\n", after, after != focusBefore)
	for _, s := range surfaces {
		s.Close()
	}
	return nil
}

// benchmark presents frames back to back, waiting for each frame callback,
// and prints commit-to-callback latency percentiles.
func benchmark(ctx context.Context, s surface, n int) {
	sz := s.NativeSize()
	sc := s.Scale()
	var lat, copyDur []time.Duration
	frames := make([]*image.RGBA, 2)
	for i := range frames {
		frames[i], _ = testFrame(sz.W, sz.H, sc, float64(sz.W)/2+float64(i)*40*sc, float64(sz.H)/2, "bench")
	}
	for i := 0; i < n; i++ {
		prev := s.LastFrameLatency()
		t := time.Now()
		if err := s.Present(ctx, frames[i%2]); err != nil {
			fmt.Println("bench present:", err)
			return
		}
		copyDur = append(copyDur, time.Since(t))
		deadline := time.Now().Add(200 * time.Millisecond)
		for s.LastFrameLatency() == prev && time.Now().Before(deadline) {
			time.Sleep(200 * time.Microsecond)
		}
		lat = append(lat, s.LastFrameLatency())
	}
	slices.Sort(lat)
	slices.Sort(copyDur)
	pct := func(d []time.Duration, p float64) time.Duration {
		return d[int(float64(len(d)-1)*p)].Round(time.Microsecond)
	}
	fmt.Printf("bench frames=%d present-call p50=%v p95=%v | commit->frame-callback p50=%v p95=%v max=%v\n",
		n, pct(copyDur, .5), pct(copyDur, .95), pct(lat, .5), pct(lat, .95), lat[len(lat)-1].Round(time.Microsecond))
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// focused returns the compositor's idea of the focused window: niri's IPC
// on niri, the X input focus otherwise.
func focused(backend string) string {
	if os.Getenv("NIRI_SOCKET") != "" {
		out, err := exec.Command("niri", "msg", "focused-window").Output()
		if err == nil {
			line, _, _ := strings.Cut(string(out), "\n")
			return strings.TrimSpace(line)
		}
	}
	if backend == "x11" {
		if xh, err := x11overlay.New(context.Background()); err == nil {
			defer xh.Close()
			if w, err := xh.FocusedWindow(); err == nil {
				return fmt.Sprintf("x11-window-0x%x", w)
			}
		}
	}
	return "unknown"
}
