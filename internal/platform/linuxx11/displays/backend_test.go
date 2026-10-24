package displays

import (
	"testing"

	"github.com/jezek/xgb/randr"

	"github.com/xlsft/openclick/pkg/contracts"
)

func TestBuildDisplays(t *testing.T) {
	crtcs := map[randr.Crtc]crtcInfo{
		1: {0, 0, 1920, 1080, randr.RotationRotate0},
		2: {1920, 0, 1080, 1920, randr.RotationRotate90 | randr.RotationReflectX},
	}
	outs := []outputInfo{
		{name: "HDMI-1", crtc: 2, conn: randr.ConnectionConnected, id: 11},
		{name: "DP-1", crtc: 1, conn: randr.ConnectionConnected, id: 10},
		{name: "DP-2", crtc: 0, conn: randr.ConnectionConnected, id: 12},     // connected but no CRTC
		{name: "VGA-1", crtc: 0, conn: randr.ConnectionDisconnected, id: 13}, // unplugged
	}
	tests := []struct {
		name    string
		primary randr.Output
		wantPri contracts.DisplayID
	}{
		{"explicit primary", 11, "HDMI-1"},
		{"none set: origin wins", 0, "DP-1"},
		{"primary not active: origin wins", 12, "DP-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := buildDisplays(outs, crtcs, tc.primary)
			if len(ds) != 2 || ds[0].ID != "DP-1" || ds[1].ID != "HDMI-1" {
				t.Fatalf("displays: %+v", ds)
			}
			for _, d := range ds {
				if d.Primary != (d.ID == tc.wantPri) {
					t.Fatalf("primary wrong: %+v", ds)
				}
			}
			if ds[1].Rotation != 90 || ds[1].Native != (contracts.PixelSize{W: 1080, H: 1920}) || ds[1].Scale != 1 {
				t.Fatalf("rotated display: %+v", ds[1])
			}
			if ds[1].Bounds != contracts.RectXYWH(1920, 0, 1080, 1920) {
				t.Fatalf("bounds: %+v", ds[1].Bounds)
			}
		})
	}
}

func TestParseXftDPI(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"Xft.antialias:\t1\nXft.dpi:\t144\nXft.hinting: 1\n", 144},
		{"Xft.dpi: 96", 96},
		{"Xft.dpi: junk", 0},
		{"Xcursor.size: 24\n", 0},
		{"", 0},
	}
	for _, tc := range tests {
		if got := parseXftDPI(tc.in); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.in, got, tc.want)
		}
	}
}
