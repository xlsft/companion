//go:build linux

package linux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/xlsft/openclick/internal/restraint"
)

// Signal names reported as restraint reasons.
const (
	NameScreenShare  = "screen-share"
	NameCall         = "call"
	NamePresentation = "presentation"
	notifService     = "org.freedesktop.Notifications"
	notifPath        = "/org/freedesktop/Notifications"
	maxCommandOutput = 32 << 20 // pw-dump on a busy graph is a few MB
)

// ErrUnknown marks "could not determine"; the monitor treats it as inactive.
var ErrUnknown = errors.New("restraint: state unknown")

// runner abstracts command execution for tests.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s not installed", ErrUnknown, name)
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = &limitWriter{w: &out, n: maxCommandOutput}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %s timed out", ErrUnknown, name)
		}
		return nil, fmt.Errorf("%w: %s failed", ErrUnknown, name)
	}
	return out.Bytes(), nil
}

type limitWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.w.Len()+len(p) > l.n {
		return 0, errors.New("output too large")
	}
	return l.w.Write(p)
}

// Options tune the Linux signal set.
type Options struct {
	OwnPID int // excluded from stream counts; defaults to os.Getpid()
	// AnyMicCountsAsCall treats every microphone capture as a call, not just
	// known call apps and Communication-role streams.
	AnyMicCountsAsCall bool
}

// Signals returns the screen share, call and presentation signals.
func Signals(o Options) []restraint.Signal {
	if o.OwnPID == 0 {
		o.OwnPID = os.Getpid()
	}
	pw := &pwSource{run: execRunner}
	return []restraint.Signal{
		&pwSignal{name: NameScreenShare, src: pw, pick: func(g *graph) int { return len(g.screenShares(o.OwnPID)) }},
		&pwSignal{name: NameCall, src: pw, pick: func(g *graph) int { return len(g.micUsers(o.OwnPID, o.AnyMicCountsAsCall)) }},
		&presentationSignal{run: execRunner, notifInhibited: notificationsInhibited, niri: os.Getenv("NIRI_SOCKET") != ""},
	}
}

// pwSource runs pw-dump once per poll for all pw signals sharing it: the
// monitor probes in parallel, so a result younger than cacheTTL is reused.
type pwSource struct {
	run runner
	now func() time.Time

	mu  sync.Mutex
	at  time.Time
	g   *graph
	err error
}

const cacheTTL = time.Second

func (s *pwSource) graph(ctx context.Context) (*graph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	if !s.at.IsZero() && now().Sub(s.at) < cacheTTL {
		return s.g, s.err
	}
	out, err := s.run(ctx, "pw-dump")
	if err == nil {
		s.g, err = parsePWDump(out)
	}
	if err != nil {
		s.g = nil
	}
	s.err, s.at = err, now()
	return s.g, s.err
}

type pwSignal struct {
	name string
	src  *pwSource
	pick func(*graph) int
}

func (s *pwSignal) Name() string { return s.name }

func (s *pwSignal) Active(ctx context.Context) (bool, error) {
	g, err := s.src.graph(ctx)
	if err != nil {
		return false, err
	}
	return s.pick(g) > 0, nil
}

// presentationSignal combines the notification daemon's Inhibited property
// with niri's focused-window fullscreen state. Read-only.
type presentationSignal struct {
	run            runner
	notifInhibited func(ctx context.Context) (bool, error)
	niri           bool
}

func (s *presentationSignal) Name() string { return NamePresentation }

func (s *presentationSignal) Active(ctx context.Context) (bool, error) {
	var errs []error
	if s.notifInhibited != nil {
		on, err := s.notifInhibited(ctx)
		if on {
			return true, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if s.niri {
		on, err := niriFullscreen(ctx, s.run)
		if on {
			return true, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return false, errors.Join(errs...)
}

// notificationsInhibited reads org.freedesktop.Notifications.Inhibited
// (spec 1.3 draft; absent on most daemons, which is reported as unknown).
func notificationsInhibited(ctx context.Context) (bool, error) {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return false, fmt.Errorf("%w: session bus unavailable", ErrUnknown)
	}
	defer conn.Close()
	v, err := conn.Object(notifService, notifPath).GetProperty(notifService + ".Inhibited")
	if err != nil {
		return false, fmt.Errorf("%w: Inhibited property unavailable", ErrUnknown)
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("%w: Inhibited has unexpected type", ErrUnknown)
	}
	return b, nil
}

type niriWindow struct {
	IsFullscreen *bool `json:"is_fullscreen"`
	Layout       struct {
		WindowSize [2]float64 `json:"window_size"`
	} `json:"layout"`
}

type niriOutput struct {
	Logical *struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"logical"`
}

// parseNiriFullscreen decides from `niri msg --json focused-window` (and, when
// the window has no is_fullscreen field, the focused output's logical size).
func parseNiriFullscreen(window, output []byte) (bool, error) {
	var w niriWindow
	if err := json.Unmarshal(window, &w); err != nil {
		return false, fmt.Errorf("%w: bad focused-window json", ErrUnknown)
	}
	if w.IsFullscreen != nil {
		return *w.IsFullscreen, nil
	}
	if len(output) == 0 {
		return false, fmt.Errorf("%w: no is_fullscreen field", ErrUnknown)
	}
	var o niriOutput
	if err := json.Unmarshal(output, &o); err != nil || o.Logical == nil {
		return false, fmt.Errorf("%w: no is_fullscreen field", ErrUnknown)
	}
	ws := w.Layout.WindowSize
	return ws[0] > 0 && ws[0] >= o.Logical.Width && ws[1] >= o.Logical.Height, nil
}

func niriFullscreen(ctx context.Context, run runner) (bool, error) {
	win, err := run(ctx, "niri", "msg", "--json", "focused-window")
	if err != nil {
		return false, err
	}
	// "null" when nothing is focused.
	if string(bytes.TrimSpace(win)) == "null" {
		return false, nil
	}
	var probe niriWindow
	var out []byte
	if json.Unmarshal(win, &probe) == nil && probe.IsFullscreen == nil {
		// Older niri: fall back to comparing with the output size.
		out, _ = run(ctx, "niri", "msg", "--json", "focused-output")
	}
	return parseNiriFullscreen(win, out)
}
