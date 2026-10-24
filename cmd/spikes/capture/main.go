// Command capture takes screenshots through the platform capture backend
// (OC-40) and prints their metadata and timings. Images stay in memory
// unless -out names a directory to write PNGs to.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"time"

	"github.com/xlsft/companion/internal/platform/capture"
	"github.com/xlsft/companion/internal/platform/detect"
	platdisplays "github.com/xlsft/companion/internal/platform/displays"
	"github.com/xlsft/companion/pkg/contracts"
)

func main() {
	session := flag.String("session", "", "override session kind: wayland or x11")
	backend := flag.String("backend", "", "force a backend: "+capture.BackendScreencopy+", "+capture.BackendPortalScreenshot+" or "+capture.BackendX11)
	n := flag.Int("n", 5, "captures per target, for latency statistics")
	out := flag.String("out", "", "directory to save PNGs of the last capture (off by default)")
	region := flag.String("region", "", "also capture a region x,y,w,h in desktop coordinates")
	flag.Parse()
	if err := run(*session, *backend, *n, *out, *region); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(session, backend string, n int, out, region string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	t0 := time.Now()
	report, err := detect.New().Detect(ctx)
	if err != nil {
		return err
	}
	if session != "" {
		report.Session = contracts.SessionKind(session)
	}
	st := report.State(contracts.CapScreenCapture)
	fmt.Printf("session=%s compositor=%s screen_capture=%s backend=%q candidates=%v (detect %v)\n",
		report.Session, report.Compositor, st.Status, st.Backend, capture.Candidates(report), since(t0))

	t0 = time.Now()
	tr, err := platdisplays.New(ctx, report)
	if err != nil {
		return err
	}
	defer tr.Close()
	fmt.Printf("displays tracker ready in %v\n", since(t0))

	t0 = time.Now()
	var c *capture.Capturer
	if backend != "" {
		c, err = capture.NewBackend(ctx, backend, tr)
	} else {
		c, err = capture.New(ctx, report, tr)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Printf("backend %s open in %v\n", c.Backend(), since(t0))

	reqs := []struct {
		name string
		req  contracts.CaptureRequest
	}{
		{"active", contracts.CaptureRequest{Target: contracts.CaptureActiveDisplay}},
		{"all", contracts.CaptureRequest{Target: contracts.CaptureAllDisplays}},
	}
	if region != "" {
		var x, y, w, h float64
		if _, err := fmt.Sscanf(region, "%g,%g,%g,%g", &x, &y, &w, &h); err != nil {
			return fmt.Errorf("bad -region: %w", err)
		}
		reqs = append(reqs, struct {
			name string
			req  contracts.CaptureRequest
		}{"region", contracts.CaptureRequest{Target: contracts.CaptureRegion, Region: contracts.RectXYWH(x, y, w, h)}})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for _, r := range reqs {
		var times []time.Duration
		var last []*contracts.Capture
		for i := 0; i < n; i++ {
			t := time.Now()
			caps, err := c.Capture(ctx, r.req)
			if err != nil {
				return fmt.Errorf("%s: %w", r.name, err)
			}
			times = append(times, time.Since(t))
			for _, cp := range last {
				cp.Release()
			}
			last = caps
		}
		fmt.Printf("\n== %s: %d captures, %s\n", r.name, len(last), stats(times))
		for i, cp := range last {
			if err := enc.Encode(cp.Meta); err != nil {
				return err
			}
			if out != "" {
				if err := save(filepath.Join(out, fmt.Sprintf("%s-%d.png", r.name, i)), cp); err != nil {
					return err
				}
			}
			cp.Release()
		}
	}
	return nil
}

func stats(ds []time.Duration) string {
	s := slices.Clone(ds)
	slices.Sort(s)
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	r := func(d time.Duration) time.Duration { return d.Round(100 * time.Microsecond) }
	return fmt.Sprintf("first=%v min=%v median=%v max=%v mean=%v",
		r(ds[0]), r(s[0]), r(s[len(s)/2]), r(s[len(s)-1]), r(sum/time.Duration(len(s))))
}

func since(t time.Time) time.Duration { return time.Since(t).Round(100 * time.Microsecond) }

func save(path string, c *contracts.Capture) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := png.Encode(f, c.Image); err != nil {
		return err
	}
	fmt.Println("saved", path)
	return f.Close()
}
