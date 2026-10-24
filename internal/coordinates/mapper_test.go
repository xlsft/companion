package coordinates

import (
	"errors"
	"math"
	"testing"

	"github.com/xlsft/openclick/pkg/contracts"
)

// Mixed-DPI layout with a negative origin:
//
//	left  (-1280,0) 1280x1024 @1    primary (0,0) 1920x1080 @2    right (1920,0) 1536x960 @1.25
var (
	left    = contracts.Display{ID: "left", Bounds: contracts.RectXYWH(-1280, 0, 1280, 1024), Native: contracts.PixelSize{W: 1280, H: 1024}, Scale: 1}
	primary = contracts.Display{ID: "primary", Bounds: contracts.RectXYWH(0, 0, 1920, 1080), Native: contracts.PixelSize{W: 3840, H: 2160}, Scale: 2, Primary: true}
	right   = contracts.Display{ID: "right", Bounds: contracts.RectXYWH(1920, 0, 1536, 960), Native: contracts.PixelSize{W: 1920, H: 1200}, Scale: 1.25}
	topo    = contracts.Topology{Version: 7, Displays: []contracts.Display{left, primary, right}}
)

func fullCapture(d contracts.Display) contracts.CaptureMetadata {
	return contracts.CaptureMetadata{
		ID:              contracts.CaptureID("cap-" + string(d.ID)),
		DisplayID:       d.ID,
		Native:          d.Native,
		Model:           contracts.PixelSize{W: d.Native.W / 2, H: d.Native.H / 2},
		Bounds:          d.Bounds,
		Scale:           d.Scale,
		TopologyVersion: topo.Version,
	}
}

func near(a, b contracts.Point) bool {
	return math.Abs(a.X-b.X) < 1e-9 && math.Abs(a.Y-b.Y) < 1e-9
}

func TestResolve(t *testing.T) {
	region := contracts.CaptureMetadata{
		ID: "region", DisplayID: "primary",
		Native: contracts.PixelSize{W: 800, H: 600}, Bounds: contracts.RectXYWH(100, 200, 400, 300),
		Scale: 2, TopologyVersion: topo.Version,
	}
	tests := []struct {
		name    string
		meta    contracts.CaptureMetadata
		p       contracts.Point
		display contracts.DisplayID
		desktop contracts.Point
		local   contracts.Point
		surface contracts.Point
	}{
		{"200% center", fullCapture(primary), contracts.Point{X: 0.5, Y: 0.5}, "primary",
			contracts.Point{X: 960, Y: 540}, contracts.Point{X: 960, Y: 540}, contracts.Point{X: 1920, Y: 1080}},
		{"negative origin", fullCapture(left), contracts.Point{X: 0.25, Y: 0.5}, "left",
			contracts.Point{X: -960, Y: 512}, contracts.Point{X: 320, Y: 512}, contracts.Point{X: 320, Y: 512}},
		{"125% fractional", fullCapture(right), contracts.Point{X: 0.5, Y: 0.5}, "right",
			contracts.Point{X: 2688, Y: 480}, contracts.Point{X: 768, Y: 480}, contracts.Point{X: 960, Y: 600}},
		{"region on 200%", region, contracts.Point{X: 0.5, Y: 0.5}, "primary",
			contracts.Point{X: 300, Y: 350}, contracts.Point{X: 300, Y: 350}, contracts.Point{X: 600, Y: 700}},
		{"far edge stays on its display", fullCapture(primary), contracts.Point{X: 1, Y: 1}, "primary",
			contracts.Point{X: 1920, Y: 1080}, contracts.Point{X: 1920, Y: 1080}, contracts.Point{X: 3840, Y: 2160}},
		{"origin corner", fullCapture(right), contracts.Point{}, "right",
			contracts.Point{X: 1920, Y: 0}, contracts.Point{}, contracts.Point{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.meta, topo, tt.p)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Display.ID != tt.display {
				t.Errorf("display = %s, want %s", got.Display.ID, tt.display)
			}
			if !near(got.Desktop, tt.desktop) || !near(got.Local, tt.local) || !near(got.Surface, tt.surface) {
				t.Errorf("got desktop %v local %v surface %v, want %v %v %v",
					got.Desktop, got.Local, got.Surface, tt.desktop, tt.local, tt.surface)
			}
		})
	}
}

func TestResolveStale(t *testing.T) {
	changed := topo
	changed.Version++
	if _, err := Resolve(fullCapture(primary), changed, contracts.Point{X: 0.5, Y: 0.5}); !errors.Is(err, contracts.ErrStale) {
		t.Fatalf("topology change: err = %v, want ErrStale", err)
	}

	unplugged := contracts.Topology{Version: topo.Version, Displays: []contracts.Display{primary}}
	if _, err := Resolve(fullCapture(left), unplugged, contracts.Point{X: 0.5, Y: 0.5}); !errors.Is(err, contracts.ErrStale) {
		t.Fatalf("display gone: err = %v, want ErrStale", err)
	}
}

func TestRotationRoundTrip(t *testing.T) {
	// Portrait screen whose capture buffer is still landscape.
	portrait := contracts.CaptureMetadata{
		ID: "rot", DisplayID: "p", Native: contracts.PixelSize{W: 1920, H: 1080},
		Bounds: contracts.RectXYWH(0, 0, 1080, 1920), Scale: 1,
	}
	for _, rot := range []int{90, 180, 270, -90} {
		m := portrait
		m.Rotation = rot
		if rot == 180 {
			m.Native = contracts.PixelSize{W: 1080, H: 1920}
		}
		for _, p := range []contracts.Point{{X: 0, Y: 0}, {X: 0.2, Y: 0.7}, {X: 1, Y: 0.5}} {
			d, err := NormalizedToDesktop(m, p)
			if err != nil {
				t.Fatalf("rot %d: %v", rot, err)
			}
			back, err := DesktopToNormalized(m, d)
			if err != nil {
				t.Fatalf("rot %d: %v", rot, err)
			}
			if !near(back, p) {
				t.Errorf("rot %d: %v -> %v -> %v", rot, p, d, back)
			}
		}
	}
	m := portrait
	m.Rotation = 90
	// The buffer's top-left lands on the screen's top-right after a
	// clockwise quarter turn.
	if d, _ := NormalizedToDesktop(m, contracts.Point{}); !near(d, contracts.Point{X: 1080, Y: 0}) {
		t.Errorf("rot 90 origin -> %v, want (1080,0)", d)
	}
}

func TestValidate(t *testing.T) {
	ok := fullCapture(right)
	if err := Validate(ok); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
	bad := ok
	bad.Scale = 1 // native 1920 does not match 1536 logical at 1x
	if err := Validate(bad); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("scale mismatch: err = %v", err)
	}
	bad = ok
	bad.Rotation = 45
	if err := Validate(bad); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("rotation 45: err = %v", err)
	}
	if _, err := NormalizedToDesktop(ok, contracts.Point{X: 1.01}); err == nil {
		t.Fatal("point outside capture accepted")
	}
}

func TestModelPixelToNormalized(t *testing.T) {
	m := fullCapture(primary) // model 1920x1080
	got, err := ModelPixelToNormalized(m, contracts.Point{X: 480, Y: 270})
	if err != nil || !near(got, contracts.Point{X: 0.25, Y: 0.25}) {
		t.Fatalf("got %v, %v", got, err)
	}
	m.Model = contracts.PixelSize{}
	if _, err := ModelPixelToNormalized(m, contracts.Point{}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("unknown model size: err = %v", err)
	}
}
