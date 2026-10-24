package capture

import (
	"bufio"
	"context"
	"errors"
	"image"
	"math"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/companion/internal/coordinates"
	"github.com/xlsft/companion/pkg/contracts"
)

// Two displays: a 1x primary at the origin and a 2x one to its right,
// rotated 90° on the second test topology.
var (
	left = contracts.Display{ID: "DP-1", Bounds: contracts.RectXYWH(0, 0, 1920, 1080),
		Native: contracts.PixelSize{W: 1920, H: 1080}, Scale: 1, Primary: true}
	right = contracts.Display{ID: "DP-2", Bounds: contracts.RectXYWH(1920, -200, 1280, 720),
		Native: contracts.PixelSize{W: 2560, H: 1440}, Scale: 2}
	topo2 = contracts.Topology{Version: 7, Displays: []contracts.Display{left, right}}
)

type fakeTopo struct {
	mu   sync.Mutex
	seq  []contracts.Topology // returned in order, last one repeats
	err  error
	read int
}

func (f *fakeTopo) Topology(context.Context) (contracts.Topology, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return contracts.Topology{}, f.err
	}
	t := f.seq[min(f.read, len(f.seq)-1)]
	f.read++
	return t, nil
}

// fakeGrabber returns images of the size a real backend would: area×scale
// in buffer orientation.
type fakeGrabber struct {
	portalLike bool // scale from the image, rotation 0
	got        []target
}

func (g *fakeGrabber) close() error { return nil }

func (g *fakeGrabber) grab(_ context.Context, _ contracts.Topology, ts []target) ([]shot, error) {
	g.got = append(g.got, ts...)
	out := make([]shot, len(ts))
	for i, t := range ts {
		s := t.display.Scale
		w, h := int(math.Round(t.area.Width()*s)), int(math.Round(t.area.Height()*s))
		rot := t.display.Rotation
		if g.portalLike {
			rot = 0
		} else if rot == 90 || rot == 270 {
			w, h = h, w
		}
		out[i] = shot{img: image.NewRGBA(image.Rect(0, 0, w, h)), scale: s, rotation: rot, ids: map[string]string{"k": "v"}}
	}
	return out, nil
}

func TestCaptureTargets(t *testing.T) {
	rotated := right
	rotated.Rotation = 90
	rotated.Native = contracts.PixelSize{W: 1440, H: 2560}
	rotated.Bounds = contracts.RectXYWH(1920, 0, 720, 1280)
	topoRot := contracts.Topology{Version: 3, Displays: []contracts.Display{left, rotated}}

	activeRight := ActiveFunc(func(context.Context, contracts.Topology) (contracts.DisplayID, error) { return "DP-2", nil })
	failing := ActiveFunc(func(context.Context, contracts.Topology) (contracts.DisplayID, error) {
		return "", errors.New("no")
	})
	unknown := ActiveFunc(func(context.Context, contracts.Topology) (contracts.DisplayID, error) { return "gone", nil })

	tests := []struct {
		name       string
		topo       contracts.Topology
		req        contracts.CaptureRequest
		active     []ActiveDisplay
		portalLike bool
		want       []contracts.Rect
		wantIDs    []contracts.DisplayID
		wantErr    error
	}{
		{name: "active falls back to primary", topo: topo2, req: contracts.CaptureRequest{Target: contracts.CaptureActiveDisplay},
			want: []contracts.Rect{left.Bounds}, wantIDs: []contracts.DisplayID{"DP-1"}},
		{name: "active from resolver", topo: topo2, req: contracts.CaptureRequest{Target: contracts.CaptureActiveDisplay},
			active: []ActiveDisplay{failing, unknown, activeRight},
			want:   []contracts.Rect{right.Bounds}, wantIDs: []contracts.DisplayID{"DP-2"}},
		{name: "all displays", topo: topo2, req: contracts.CaptureRequest{Target: contracts.CaptureAllDisplays},
			want: []contracts.Rect{left.Bounds, right.Bounds}, wantIDs: []contracts.DisplayID{"DP-1", "DP-2"}},
		{name: "one display", topo: topo2, req: contracts.CaptureRequest{Target: contracts.CaptureDisplay, Display: "DP-2"},
			want: []contracts.Rect{right.Bounds}, wantIDs: []contracts.DisplayID{"DP-2"}},
		{name: "missing display is stale", topo: topo2, req: contracts.CaptureRequest{Target: contracts.CaptureDisplay, Display: "HDMI-A-1"},
			wantErr: contracts.ErrStale},
		{name: "region rounded outward", topo: topo2,
			req:  contracts.CaptureRequest{Target: contracts.CaptureRegion, Region: contracts.Rect{Min: contracts.Point{X: 10.5, Y: 20.2}, Max: contracts.Point{X: 110.1, Y: 70.9}}},
			want: []contracts.Rect{contracts.RectXYWH(10, 20, 101, 51)}, wantIDs: []contracts.DisplayID{"DP-1"}},
		{name: "region across displays goes to the larger overlap", topo: topo2,
			req:  contracts.CaptureRequest{Target: contracts.CaptureRegion, Region: contracts.RectXYWH(1900, 0, 400, 100)},
			want: []contracts.Rect{contracts.RectXYWH(1920, 0, 380, 100)}, wantIDs: []contracts.DisplayID{"DP-2"}},
		{name: "region outside", topo: topo2,
			req: contracts.CaptureRequest{Target: contracts.CaptureRegion, Region: contracts.RectXYWH(-500, -500, 10, 10)}, wantErr: errAny},
		{name: "empty region", topo: topo2,
			req: contracts.CaptureRequest{Target: contracts.CaptureRegion}, wantErr: errAny},
		{name: "rotated display keeps buffer orientation", topo: topoRot,
			req:  contracts.CaptureRequest{Target: contracts.CaptureDisplay, Display: "DP-2"},
			want: []contracts.Rect{rotated.Bounds}, wantIDs: []contracts.DisplayID{"DP-2"}},
		{name: "rotated region", topo: topoRot,
			req:  contracts.CaptureRequest{Target: contracts.CaptureRegion, Region: contracts.RectXYWH(2000, 100, 300, 50)},
			want: []contracts.Rect{contracts.RectXYWH(2000, 100, 300, 50)}, wantIDs: []contracts.DisplayID{"DP-2"}},
		{name: "portal-like rotation 0", topo: topoRot, portalLike: true,
			req:  contracts.CaptureRequest{Target: contracts.CaptureAllDisplays},
			want: []contracts.Rect{left.Bounds, rotated.Bounds}, wantIDs: []contracts.DisplayID{"DP-1", "DP-2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &fakeGrabber{portalLike: tt.portalLike}
			c := &Capturer{backend: "fake", topo: &fakeTopo{seq: []contracts.Topology{tt.topo}}, g: g, active: tt.active}
			caps, err := c.Capture(context.Background(), tt.req)
			if tt.wantErr != nil {
				if err == nil || (tt.wantErr != errAny && !errors.Is(err, tt.wantErr)) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(caps) != len(tt.want) {
				t.Fatalf("%d captures, want %d", len(caps), len(tt.want))
			}
			origin := desktopOrigin(tt.topo)
			for i, cp := range caps {
				m := cp.Meta
				if m.Bounds != tt.want[i] || m.DisplayID != tt.wantIDs[i] {
					t.Errorf("capture %d: bounds %v display %q, want %v %q", i, m.Bounds, m.DisplayID, tt.want[i], tt.wantIDs[i])
				}
				if err := coordinates.Validate(m); err != nil {
					t.Errorf("capture %d: %v", i, err)
				}
				if m.TopologyVersion != tt.topo.Version || m.DesktopOrigin != origin || m.Timestamp.IsZero() {
					t.Errorf("capture %d: version %d origin %v ts %v", i, m.TopologyVersion, m.DesktopOrigin, m.Timestamp)
				}
				if m.PlatformIDs["backend"] != "fake" || m.PlatformIDs["k"] != "v" {
					t.Errorf("capture %d: platform ids %v", i, m.PlatformIDs)
				}
				if cp.Image == nil || cp.Image.Bounds().Dx() != m.Native.W {
					t.Errorf("capture %d: image/native mismatch", i)
				}
			}
		})
	}
}

var errAny = errors.New("any error")

func TestCaptureIDsUnique(t *testing.T) {
	c := &Capturer{backend: "fake", topo: &fakeTopo{seq: []contracts.Topology{topo2}}, g: &fakeGrabber{}}
	seen := map[contracts.CaptureID]bool{}
	for i := 0; i < 50; i++ {
		caps, err := c.Capture(context.Background(), contracts.CaptureRequest{Target: contracts.CaptureAllDisplays})
		if err != nil {
			t.Fatal(err)
		}
		for _, cp := range caps {
			if seen[cp.Meta.ID] {
				t.Fatalf("duplicate id %s", cp.Meta.ID)
			}
			seen[cp.Meta.ID] = true
		}
	}
}

func TestCaptureTopologyRace(t *testing.T) {
	v8 := topo2
	v8.Version = 8
	v9 := topo2
	v9.Version = 9
	tests := []struct {
		name    string
		seq     []contracts.Topology
		wantVer uint64
		wantErr error
	}{
		// before/after reads per attempt: (7,8) moved, then (8,8) ok.
		{"one change is retried", []contracts.Topology{topo2, v8}, 8, nil},
		{"changing twice is stale", []contracts.Topology{topo2, v8, v8, v9}, 0, contracts.ErrStale},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Capturer{backend: "fake", topo: &fakeTopo{seq: tt.seq}, g: &fakeGrabber{}}
			caps, err := c.Capture(context.Background(), contracts.CaptureRequest{Target: contracts.CaptureActiveDisplay})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || caps[0].Meta.TopologyVersion != tt.wantVer {
				t.Fatalf("err = %v caps = %+v", err, caps)
			}
		})
	}
}

func TestCaptureNoDisplays(t *testing.T) {
	c := &Capturer{backend: "fake", topo: &fakeTopo{seq: []contracts.Topology{{Version: 1}}}, g: &fakeGrabber{}}
	if _, err := c.Capture(context.Background(), contracts.CaptureRequest{}); !errors.Is(err, contracts.ErrStale) {
		t.Fatalf("err = %v", err)
	}
}

func TestBufferRotation(t *testing.T) {
	rot := contracts.Display{Bounds: contracts.RectXYWH(0, 0, 1080, 1920), Scale: 1, Rotation: 90}
	flip := contracts.Display{Bounds: contracts.RectXYWH(0, 0, 100, 50), Scale: 1, Rotation: 180}
	tests := []struct {
		name string
		d    contracts.Display
		w, h int
		want int
	}{
		{"untransformed buffer keeps rotation", rot, 1920, 1080, 90},
		{"compositor already rotated", rot, 1080, 1920, 0},
		{"180 follows the protocol", flip, 100, 50, 180},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bufferRotation(whole(tt.d), tt.w, tt.h); got != tt.want {
				t.Fatalf("got %d want %d", got, tt.want)
			}
		})
	}
}

func TestCandidates(t *testing.T) {
	tests := []struct {
		name string
		r    contracts.CapabilityReport
		want []string
	}{
		{"niri", contracts.CapabilityReport{Session: contracts.SessionWayland,
			Facts: map[string]string{"wayland.screencopy": "true", "portal.Screenshot.version": "2"}},
			[]string{BackendScreencopy, BackendPortalScreenshot}},
		{"gnome", contracts.CapabilityReport{Session: contracts.SessionWayland,
			Facts: map[string]string{"wayland.screencopy": "false", "portal.Screenshot.version": "2"}},
			[]string{BackendPortalScreenshot}},
		{"registry not probed, no bus", contracts.CapabilityReport{Session: contracts.SessionWayland,
			Facts: map[string]string{"portal.Screenshot.version": "absent"}},
			[]string{BackendScreencopy}},
		{"wayland without anything", contracts.CapabilityReport{Session: contracts.SessionWayland,
			Facts: map[string]string{"wayland.screencopy": "false", "portal.Screenshot.version": "absent"}},
			nil},
		{"x11", contracts.CapabilityReport{Session: contracts.SessionX11}, []string{BackendX11}},
		{"headless", contracts.CapabilityReport{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Candidates(tt.r); !slices.Equal(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestChoose(t *testing.T) {
	ok := func(context.Context) (grabber, []ActiveDisplay, error) { return &fakeGrabber{}, nil, nil }
	unsupported := func(context.Context) (grabber, []ActiveDisplay, error) {
		return nil, nil, contracts.Unsupported(contracts.CapScreenCapture)
	}
	broken := func(context.Context) (grabber, []ActiveDisplay, error) {
		return nil, nil, errors.New("connect refused")
	}
	openers := map[string]opener{"a": unsupported, "b": broken, "c": ok, "d": ok}
	tests := []struct {
		name    string
		names   []string
		want    string
		wantErr bool
	}{
		{"first works", []string{"c", "d"}, "c", false},
		{"falls through failures", []string{"a", "b", "d"}, "d", false},
		{"none works", []string{"a", "b"}, "", true},
		{"no candidates", nil, "", true},
		{"unknown names skipped", []string{"zzz", "c"}, "c", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := choose(context.Background(), tt.names, openers, &fakeTopo{seq: []contracts.Topology{topo2}}, nil)
			if tt.wantErr {
				var ce *contracts.CapabilityError
				if !errors.Is(err, contracts.ErrUnsupported) || !errors.As(err, &ce) || ce.Capability != contracts.CapScreenCapture {
					t.Fatalf("err = %v, want Unsupported(screen_capture)", err)
				}
				return
			}
			if err != nil || c.Backend() != tt.want {
				t.Fatalf("got %v %v, want %s", c, err, tt.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := map[string]opener{"x": func(ctx context.Context) (grabber, []ActiveDisplay, error) { return nil, nil, ctx.Err() }}
	if _, err := choose(ctx, []string{"x"}, cancelled, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: err = %v", err)
	}
}

func TestParseNiriFocused(t *testing.T) {
	tests := []struct {
		in      string
		want    contracts.DisplayID
		wantErr bool
	}{
		{`{"Ok":{"FocusedOutput":{"name":"DP-2","make":"x","logical":{"x":0}}}}`, "DP-2", false},
		{`{"Ok":{"FocusedOutput":null}}`, "", true},
		{`{"Err":"boom"}`, "", true},
		{`not json`, "", true},
	}
	for _, tt := range tests {
		got, err := parseNiriFocused([]byte(tt.in))
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%s: got %q %v", tt.in, got, err)
		}
	}
}

func TestNiriActiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "niri.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		if strings.TrimSpace(line) != `"FocusedOutput"` {
			c.Write([]byte(`{"Err":"bad request"}` + "\n"))
			return
		}
		c.Write([]byte(`{"Ok":{"FocusedOutput":{"name":"HDMI-A-1"}}}` + "\n"))
	}()
	id, err := NiriActive{Socket: path}.Active(context.Background(), topo2)
	if err != nil || id != "HDMI-A-1" {
		t.Fatalf("got %q %v", id, err)
	}
	if _, err := (NiriActive{Socket: filepath.Join(t.TempDir(), "none")}).Active(context.Background(), topo2); err == nil {
		t.Fatal("missing socket must fail")
	}
}
