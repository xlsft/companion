package icon

import (
	"encoding/json"
	"testing"

	"gioui.org/f32"
)

func TestPathParsing(t *testing.T) {
	tests := []struct {
		name     string
		d        string
		contours int
		segs     int
		wantErr  bool
	}{
		{"rect", "M0 0H10V10H0z", 1, 4, false},
		{"relative implicit lineto", "m1 1 2 0 0 2z", 1, 3, false},
		{"open subpath closes", "M0 0L4 0L4 4", 1, 3, false},
		{"two subpaths", "M0 0h4v4z M10 10h4v4z", 2, 6, false},
		{"packed arc flags", "M0 0a1 1 0 011 1z", 1, 2, false},
		{"minified numbers", "M0 0l1.5.5-1-1z", 1, 3, false},
		{"smooth and quad", "M0 0C1 1 2 1 3 0S5-1 6 0Q7 1 8 0T10 0z", 1, 5, false},
		{"no command", "0 0", 0, 0, true},
		{"bad flag", "M0 0a1 1 0 2 1 1 1", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs, err := parsePath(tt.d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr {
				return
			}
			n := 0
			for _, c := range cs {
				n += len(c)
			}
			if len(cs) != tt.contours || n != tt.segs {
				t.Errorf("got %d contours, %d segs", len(cs), n)
			}
		})
	}
}

// A half circle arc must end where asked and bulge by its radius.
func TestArcGeometry(t *testing.T) {
	segs := arc(f32.Pt(0, 0), f32.Pt(10, 0), 5, 5, 0, false, true)
	last := segs[len(segs)-1].p1
	if last != f32.Pt(10, 0) {
		t.Fatalf("end %v", last)
	}
	mid := segs[0].p1
	if d := mid.Sub(f32.Pt(5, 0)); d.X*d.X+d.Y*d.Y < 24 || d.X*d.X+d.Y*d.Y > 26 {
		t.Errorf("arc midpoint %v not on the circle", mid)
	}
}

// After orientation fixing, non-zero filling must cover the same pixels
// the SVG's even-odd rule does, otherwise holes (the inside of a ring,
// a microphone's body) render filled.
func TestNonzeroMatchesEvenOdd(t *testing.T) {
	var raw map[Name][]string
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for n, paths := range raw {
		var orig []contour
		for _, d := range paths {
			c, err := parsePath(d)
			if err != nil {
				t.Fatalf("%s: %v", n, err)
			}
			orig = append(orig, c...)
		}
		fixed := contours(n)
		var origPolys, fixedPolys [][]f32.Point
		for _, c := range orig {
			origPolys = append(origPolys, flatten(c))
		}
		for _, c := range fixed {
			fixedPolys = append(fixedPolys, flatten(c))
		}
		filled, mismatch := 0, 0
		const grid = 96
		for y := range grid {
			for x := range grid {
				pt := f32.Pt((float32(x)+0.5)*24/grid, (float32(y)+0.5)*24/grid)
				eo := false
				for _, p := range origPolys {
					if inside(p, pt) {
						eo = !eo
					}
				}
				w := 0
				for _, p := range fixedPolys {
					w += winding(p, pt)
				}
				if eo {
					filled++
				}
				if eo != (w != 0) {
					mismatch++
				}
			}
		}
		if filled == 0 {
			t.Errorf("%s: draws nothing", n)
		}
		if mismatch > filled/100 {
			t.Errorf("%s: %d of %d filled samples differ between even-odd and non-zero", n, mismatch, filled)
		}
	}
}

func TestNamesExist(t *testing.T) {
	for _, n := range []Name{Add, Aiming2, Alert, ArrowRight, ArrowUp, Attachment, Brain, Chat1, CheckCircle, Check,
		CloseCircle, Close, Computer, Copy2, Dashboard2, Delete2, Down, Download2, Edit, Exit, EyeClose, Eye,
		Heartbeat, History, Information, Key2, Keyboard, Layers, Left, ListCheck, Loading3, Lock, Mic, More2,
		PauseFill, Pin, PlayFill, Refresh2, Right, Robot, Search, Settings3, Sparkles, StopFill, Time,
		Translate, Up, Volume, Warning,
		BrandAnthropic, BrandOpenAI, BrandGemini, BrandOpenRouter, BrandMistral, BrandOllama, BrandElevenLabs} {
		if len(contours(n)) == 0 {
			t.Errorf("%s has no contours", n)
		}
	}
}

func winding(poly []f32.Point, pt f32.Point) int {
	w := 0
	for i := range poly {
		a, b := poly[i], poly[(i+1)%len(poly)]
		cross := (b.X-a.X)*(pt.Y-a.Y) - (pt.X-a.X)*(b.Y-a.Y)
		if a.Y <= pt.Y && b.Y > pt.Y && cross > 0 {
			w++
		} else if a.Y > pt.Y && b.Y <= pt.Y && cross < 0 {
			w--
		}
	}
	return w
}
