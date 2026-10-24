package overlay

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

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
			name:   "spotlight dims all but a soft circle",
			shapes: []shape{{kind: KindSpotlight, pts: []contracts.Point{pt(200, 150)}, radius: 50, scale: 1}},
			probes: []probe{{200, 150, "clear"}, {10, 10, "dim"}, {390, 290, "dim"}},
		},
		{
			name:   "highlight is a translucent marker",
			shapes: []shape{{kind: KindHighlight, pts: []contracts.Point{pt(100, 100), pt(300, 130)}, scale: 1}},
			probes: []probe{{200, 115, "tint"}, {200, 160, "clear"}},
		},
		{
			name:   "underline",
			shapes: []shape{{kind: KindUnderline, pts: []contracts.Point{pt(100, 200), pt(300, 200)}, scale: 1}},
			probes: []probe{{200, 200, "accent"}, {200, 190, "clear"}},
		},
		{
			name:   "brackets mark corners only",
			shapes: []shape{{kind: KindBrackets, pts: []contracts.Point{pt(100, 100), pt(300, 200)}, scale: 1}},
			probes: []probe{{100, 105, "accent"}, {105, 100, "accent"}, {200, 100, "clear"}, {200, 150, "clear"}},
		},
		{
			name:   "badge is a filled disc with a light number",
			shapes: []shape{{kind: KindBadge, pts: []contracts.Point{pt(200, 150)}, text: "1", scale: 1}},
			probes: []probe{{190, 150, "accent"}, {200, 150, "light"}, {200, 180, "clear"}},
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
					ok = c.A > 200 && c.B > 200 && c.R > 100 && c.G < 120
				case "faint":
					ok = c.A > 0 && c.A < 200
				case "dark":
					ok = c.A > 200 && c.R < 60 && c.G < 60 && c.B < 60
				case "plate":
					ok = c.A > 200 && c.R < 40
				case "dim":
					ok = c.A > 50 && c.A < 140 && c.R == 0
				case "tint":
					ok = c.A > 40 && c.A < 120 && c.B > c.G
				case "light":
					ok = c.A > 200 && c.R > 220 && c.G > 220 && c.B > 220
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

func TestAnimAt(t *testing.T) {
	born := time.Unix(100, 0)
	tests := []struct {
		age                time.Duration
		appearLo, appearHi float64
		phase              float64
	}{
		{0, 0, 0, 0},
		{appearDur / 2, 0.8, 0.9, float64(appearDur/2) / float64(glintPeriod)},
		{appearDur, 1, 1, float64(appearDur) / float64(glintPeriod)},
		{glintPeriod + glintPeriod/4, 1, 1, 0.25},
	}
	for _, tt := range tests {
		a := animAt(born, born.Add(tt.age))
		if a.appear < tt.appearLo || a.appear > tt.appearHi || math.Abs(a.phase-tt.phase) > 1e-9 || !a.live {
			t.Errorf("age %v: %+v", tt.age, a)
		}
	}
}

// A hint is invisible at the moment it appears, then fades in, and the
// glint brightens part of its stroke towards white.
func TestRenderAnimated(t *testing.T) {
	born := time.Unix(100, 0)
	ring := []shape{{kind: KindRing, pts: []contracts.Point{pt(200, 150)}, radius: 40, scale: 1, born: born}}
	f := image.NewRGBA(image.Rect(0, 0, 400, 300))
	renderInto(f, ring, born, true, f.Bounds())
	for i := 3; i < len(f.Pix); i += 4 {
		if f.Pix[i] != 0 {
			t.Fatal("hint visible at age 0")
		}
	}
	// At phase 0 the glint is at the top of the ring.
	renderInto(f, ring, born.Add(glintPeriod*2), true, f.Bounds())
	top, side := f.RGBAAt(200, 110), f.RGBAAt(240, 150)
	if top.G <= side.G+40 {
		t.Errorf("glint not brighter: top %v, side %v", top, side)
	}
}

func TestSubpaths(t *testing.T) {
	sq := []contracts.Point{pt(0, 0), pt(10, 0), pt(10, 10), pt(0, 10)}
	tests := []struct {
		name     string
		closed   bool
		from, to float64
		parts    int
	}{
		{"open middle", false, 0.25, 0.75, 1},
		{"open clipped", false, -0.5, 0.2, 1},
		{"empty", false, 0.5, 0.5, 0},
		{"closed wraps past end", true, 0.9, 1.1, 2},
		{"closed wraps before start", true, -0.1, 0.1, 2},
	}
	for _, tt := range tests {
		if got := subpaths(sq, tt.closed, tt.from, tt.to); len(got) != tt.parts {
			t.Errorf("%s: %d parts", tt.name, len(got))
		}
	}
}

// Blitting the cached static layer must look like drawing it in place.
func TestCachedStaticLayerMatchesDirect(t *testing.T) {
	born := time.Unix(100, 0)
	now := born.Add(1234 * time.Millisecond)
	mk := func(cache bool) []shape {
		ss := []shape{
			{kind: KindRing, pts: []contracts.Point{pt(100, 100)}, radius: 30, style: Style{Emphasis: true}},
			{kind: KindRect, pts: []contracts.Point{pt(150, 40), pt(380, 120)}},
			{kind: KindHighlight, pts: []contracts.Point{pt(40, 200), pt(200, 230)}},
			{kind: KindArrow, pts: []contracts.Point{pt(300, 280), pt(220, 180)}},
			{kind: KindBadge, pts: []contracts.Point{pt(350, 250)}, text: "3"},
		}
		for i := range ss {
			ss[i].scale, ss[i].born = 1, born
			if cache {
				ss[i].cache = &shapeCache{}
			}
		}
		return ss
	}
	direct := image.NewRGBA(image.Rect(0, 0, 400, 300))
	renderInto(direct, mk(false), now, true, image.Rectangle{})
	cached := image.NewRGBA(image.Rect(0, 0, 400, 300))
	shapes := mk(true)
	renderInto(cached, shapes, now, true, image.Rectangle{}) // builds caches
	renderInto(cached, shapes, now, true, cached.Bounds())   // uses them
	diff := 0
	for i := range direct.Pix {
		d := int(direct.Pix[i]) - int(cached.Pix[i])
		if d > 2 || d < -2 {
			diff++
		}
	}
	if diff > len(direct.Pix)/1000 {
		t.Errorf("%d channel values differ between cached and direct rendering", diff)
	}
}

// Once a scrim has settled it is redrawn identically, so only the moving
// parts count as changed: a spotlight must not re-upload the whole screen
// every frame.
func TestVolatileAreaExcludesSettledScrim(t *testing.T) {
	born := time.Unix(100, 0)
	shapes := []shape{{kind: KindSpotlight, pts: []contracts.Point{pt(200, 150)}, radius: 40, scale: 1, born: born, cache: &shapeCache{}}}
	f := image.NewRGBA(image.Rect(0, 0, 400, 300))
	drawn, volatile := renderInto(f, shapes, born.Add(time.Second), true, image.Rectangle{})
	if drawn != f.Bounds() {
		t.Errorf("drawn %v, want the whole frame", drawn)
	}
	if volatile.Dx() > 120 || volatile.Dy() > 120 {
		t.Errorf("volatile %v covers more than the spotlight edge", volatile)
	}
	// During the entrance the scrim fades, so all of it changes.
	_, volatile = renderInto(f, shapes, born.Add(appearDur/2), true, drawn)
	if volatile != f.Bounds() {
		t.Errorf("entering volatile %v, want the whole frame", volatile)
	}
}

// The glint reaches the arrow's head, not only its shaft.
func TestArrowGlintLightsHead(t *testing.T) {
	born := time.Unix(100, 0)
	arrowAt := func(phase float64) color.RGBA {
		s := []shape{{kind: KindArrow, pts: []contracts.Point{pt(50, 150), pt(350, 150)}, scale: 1, born: born}}
		f := image.NewRGBA(image.Rect(0, 0, 400, 300))
		renderInto(f, s, born.Add(glintPeriod*3+time.Duration(phase*float64(glintPeriod))), true, image.Rectangle{})
		return f.RGBAAt(337, 155) // inside the head, off the shaft line
	}
	away, over := arrowAt(0.3), arrowAt(0.97)
	if over.G <= away.G+40 {
		t.Errorf("head not lit by the glint: away %v, over %v", away, over)
	}
}
