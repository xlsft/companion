package overlay

import (
	"image"
	"testing"

	"github.com/xlsft/companion/pkg/contracts"
)

func pt(x, y float64) contracts.Point { return contracts.Point{X: x, Y: y} }

// Pixel probes against the prototypes' look; positions are native pixels
// on a 400×300 frame at scale 1 unless the case says otherwise.
func TestRenderShapes(t *testing.T) {
	type probe struct {
		x, y int
		want string // "clear", "accent", "faint" (0 < alpha < 200), "dark", "plate", "dim"
	}
	tests := []struct {
		name   string
		shapes []shape
		probes []probe
	}{
		{
			name:   "plain ring is a single stroke",
			shapes: []shape{{kind: KindRing, pts: []contracts.Point{pt(200, 150)}, radius: 40, scale: 1}},
			probes: []probe{{200, 150, "clear"}, {240, 150, "accent"}, {246, 150, "clear"}, {252, 150, "clear"}},
		},
		{
			name:   "emphasised ring glows outwards only",
			shapes: []shape{{kind: KindRing, pts: []contracts.Point{pt(200, 150)}, radius: 40, scale: 1, style: Style{Emphasis: true}}},
			probes: []probe{
				{240, 150, "accent"},
				{246, 150, "faint"}, // glow just outside the stroke
				{233, 150, "clear"}, // no glow into the hole
				{270, 150, "clear"}, // glow has faded
			},
		},
		{
			name:   "dashed circle has gaps",
			shapes: []shape{{kind: KindCircle, pts: []contracts.Point{pt(200, 150)}, radius: 40, scale: 1, style: Style{Dashed: true}}},
			probes: []probe{{240, 151, "accent"}, {200, 150, "clear"}},
		},
		{
			name:   "cursor is the logo arrow, tip on the point",
			shapes: []shape{{kind: KindPoint, pts: []contracts.Point{pt(100, 100)}, scale: 1}},
			probes: []probe{
				{102, 108, "accent"}, // left arm of the head
				{108, 102, "accent"}, // top arm of the head
				{98, 98, "clear"},    // no outline around the tip
				{116, 103, "clear"},  // between the head's arms and the shaft
				{90, 90, "clear"},
			},
		},
		{
			name:   "label plate",
			shapes: []shape{{kind: KindLabel, pts: []contracts.Point{pt(50, 150)}, text: "Метроном", scale: 1}},
			probes: []probe{{53, 150, "plate"}, {50, 120, "clear"}},
		},
		{
			name:   "label near the right edge stays on screen",
			shapes: []shape{{kind: KindLabel, pts: []contracts.Point{pt(395, 150)}, text: "Экспорт проекта", scale: 1}},
			probes: []probe{{398, 137, "plate"}, {300, 137, "plate"}},
		},
		{
			name:   "emphasised rect dims the rest",
			shapes: []shape{{kind: KindRect, pts: []contracts.Point{pt(100, 100), pt(300, 200)}, scale: 1, style: Style{Emphasis: true}}},
			probes: []probe{{10, 10, "dim"}, {200, 150, "clear"}, {100, 150, "accent"}},
		},
		{
			name:   "hidpi scales the stroke",
			shapes: []shape{{kind: KindRing, pts: []contracts.Point{pt(200, 150)}, radius: 40, scale: 2}},
			probes: []probe{{243, 150, "accent"}, {237, 150, "accent"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := render(contracts.PixelSize{W: 400, H: 300}, tt.shapes)
			for _, p := range tt.probes {
				c := f.RGBAAt(p.x, p.y)
				var ok bool
				switch p.want {
				case "clear":
					ok = c.A == 0
				case "accent":
					ok = c.A > 200 && c.R > 200 && c.G > 100 && c.B < 60
				case "faint":
					ok = c.A > 0 && c.A < 200
				case "dark":
					ok = c.A > 200 && c.R < 60 && c.G < 60 && c.B < 60
				case "plate":
					ok = c.A > 200 && c.R < 40
				case "dim":
					ok = c.A > 50 && c.A < 90 && c.R == 0
				}
				if !ok {
					t.Errorf("(%d,%d) = %v, want %s", p.x, p.y, c, p.want)
				}
			}
		})
	}
}

// Shapes partly off the frame must clip, not panic.
func TestRenderClipsOffscreen(t *testing.T) {
	shapes := []shape{
		{kind: KindRing, pts: []contracts.Point{pt(-30, -30)}, radius: 40, scale: 1, style: Style{Emphasis: true}},
		{kind: KindArrow, pts: []contracts.Point{pt(390, 290), pt(900, 900)}, scale: 1},
		{kind: KindPoint, pts: []contracts.Point{pt(399, 299)}, scale: 1},
		{kind: KindLabel, pts: []contracts.Point{pt(-100, -100)}, text: "x", scale: 1},
		{kind: KindFreehand, pts: []contracts.Point{pt(-10, 5), pt(500, 5)}, scale: 1},
	}
	f := render(contracts.PixelSize{W: 400, H: 300}, shapes)
	if f.Bounds() != image.Rect(0, 0, 400, 300) {
		t.Fatal(f.Bounds())
	}
}
