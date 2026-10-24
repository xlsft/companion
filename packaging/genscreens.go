//go:build ignore

// genscreens renders the README visuals from raw window captures:
//
//   - screenshots.png, a 2× collage: Companion's hint and the spoken
//     question over a real application in front, Companion's own windows
//     staggered behind it, on banner B's backdrop;
//   - demo.gif, the same scene animated: the question appears, then the
//     overlay engine draws the ring, the logo cursor and the label with its
//     real entrance and shimmer, in a loop.
//
// The hint is drawn by internal/overlay itself, onto a frame instead of a
// screen, so it looks exactly as it does over other windows.
//
//	go run packaging/genscreens.go -raw DIR -out docs/assets
//
// DIR holds 1180×760 window captures: app.png (the real application),
// chat-dark.png and guide-light.png (from cmd/spikes/mainui); how to take
// them is in docs/development.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/xlsft/companion/internal/assets/fonts"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/pkg/contracts"
)

const winW, winH = 1180, 760

// scene is what Companion shows over the application capture: the user's
// question and the point_at call the model answers it with, in window
// pixels.
var scene = struct {
	question   string
	bubble     image.Point // top-left of the question bubble
	target     image.Point
	radius     float64 // fraction of the width, as the model sends it
	label      string
	gifW, gifH int
}{
	question: "How do I add a sphere?",
	bubble:   image.Pt(330, 84),
	target:   image.Pt(269, 38),
	radius:   0.024,
	label:    "Add → Mesh → UV Sphere",
	gifW:     960,
}

func main() {
	raw := flag.String("raw", "", "directory with the raw window captures")
	out := flag.String("out", "docs/assets", "output directory")
	flag.Parse()
	app, chat, guide := mustRead(*raw, "app.png"), mustRead(*raw, "chat-dark.png"), mustRead(*raw, "guide-light.png")

	hint, err := hintFrames(false, 0, 0)
	if err != nil {
		fail(err)
	}
	front := image.NewRGBA(app.Bounds())
	draw.Draw(front, front.Bounds(), app, image.Point{}, draw.Src)
	bubble(front, scene.bubble, 1, 1)
	draw.Draw(front, front.Bounds(), hint[0], image.Point{}, draw.Over)
	writePNG(filepath.Join(*out, "screenshots.png"), collage(front, chat, guide))

	if err := writeGIF(filepath.Join(*out, "demo.gif"), app); err != nil {
		fail(err)
	}
}

// collage stacks the windows at 2× on a 1280×720 layout: Companion's chat
// back left, its walkthrough back right, the application in front.
func collage(front, backL, backR image.Image) *image.RGBA {
	const w, h = 2560, 1440
	dst := backdrop(w, h)
	place(dst, backL, image.Pt(90, 150), 1.0)
	place(dst, backR, image.Pt(w-90-winW, 90), 1.0)
	place(dst, front, image.Pt((w-1534)/2, 400), 1.3)
	return dst
}

// backdrop is banner B's background, hsl(240 6% 7%), with a soft violet
// glow behind the front window.
func backdrop(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	base := hsl(240, 6, 7)
	glow := hsl(258, 45, 18)
	for y := range h {
		for x := range w {
			dx, dy := (float64(x)-float64(w)*0.5)/(float64(w)*0.5), (float64(y)-float64(h)*0.62)/(float64(h)*0.62)
			t := math.Max(0, 1-math.Hypot(dx, dy))
			t = t * t * (3 - 2*t)
			// Ordered dither keeps the gradient from banding in the PNG
			// and the GIF palette.
			n := (bayer[y%4][x%4] - 7.5) / 16
			mix := func(a, b uint8) uint8 {
				return uint8(math.Max(0, math.Min(255, float64(a)+(float64(b)-float64(a))*t+n)))
			}
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = mix(base.R, glow.R), mix(base.G, glow.G), mix(base.B, glow.B), 255
		}
	}
	return img
}

var bayer = [4][4]float64{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}

// place draws win scaled by k at at, with 12px corners, a 1px border and a
// deep soft shadow, all at 2×.
func place(dst *image.RGBA, win image.Image, at image.Point, k float64) {
	b := win.Bounds()
	r := image.Rect(0, 0, int(float64(b.Dx())*k), int(float64(b.Dy())*k)).Add(at)
	scaled := image.NewRGBA(r)
	xdraw.CatmullRom.Scale(scaled, r, win, b, draw.Src, nil)

	sh := image.NewAlpha(dst.Bounds())
	fillRound(sh, r.Add(image.Pt(0, 60)), 24, color.Alpha{A: 255})
	blur(sh, 50)
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(color.NRGBA{A: 190}), image.Point{}, sh, image.Point{}, draw.Over)

	ring := image.NewAlpha(dst.Bounds())
	fillRound(ring, r.Inset(-2), 26, color.Alpha{A: 255})
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(hsl(240, 5, 18)), image.Point{}, ring, image.Point{}, draw.Over)
	mask := image.NewAlpha(dst.Bounds())
	fillRound(mask, r, 24, color.Alpha{A: 255})
	draw.DrawMask(dst, r, scaled, r.Min, mask, r.Min, draw.Over)
}

// hintFrames runs the overlay engine over a window-sized display and
// returns what it presents. Without motion that is one settled frame; with
// motion it samples the engine's own animation every step for total.
func hintFrames(motion bool, step, total time.Duration) ([]*image.RGBA, error) {
	ctx := context.Background()
	d := contracts.Display{ID: "shot", Native: contracts.PixelSize{W: winW, H: winH}, Bounds: contracts.RectXYWH(0, 0, winW, winH), Scale: 1, Primary: true}
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{d}}
	h := &host{}
	e := overlay.NewEngine(h, func(context.Context) (contracts.Topology, error) { return topo, nil })
	e.SetMotion(motion)
	defer e.Clear(ctx)
	meta := contracts.CaptureMetadata{ID: "shot", DisplayID: d.ID, Native: d.Native, Bounds: d.Bounds, Scale: 1, TopologyVersion: 1, Timestamp: time.Now()}
	e.RegisterCapture(meta)
	cc := overlay.CallContext{Session: contracts.SessionRef{ID: "shot", Generation: 1}, Capture: meta.ID, CallID: "point", Segment: -1}
	args, _ := json.Marshal(map[string]any{
		"x": float64(scene.target.X) / winW, "y": float64(scene.target.Y) / winH,
		"radius": scene.radius, "label": scene.label,
	})
	cmds, err := overlay.FromToolCall(cc, overlay.ToolPointAt, args)
	if err != nil {
		return nil, err
	}
	for _, c := range cmds {
		if err := e.Apply(ctx, c); err != nil {
			return nil, err
		}
	}
	if !motion {
		return []*image.RGBA{h.snapshot()}, nil
	}
	var frames []*image.RGBA
	for t := time.Duration(0); t < total; t += step {
		frames = append(frames, h.snapshot())
		time.Sleep(step)
	}
	return frames, nil
}

// host keeps a copy of the last frame the engine presents instead of
// showing it; the engine reuses its frame buffer.
type host struct {
	mu   sync.Mutex
	last *image.RGBA
}

func (h *host) Open(context.Context, contracts.DisplayID) (contracts.OverlaySurface, error) {
	return surface{h}, nil
}

func (h *host) snapshot() *image.RGBA {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := image.NewRGBA(image.Rect(0, 0, winW, winH))
	if h.last != nil {
		copy(c.Pix, h.last.Pix)
	}
	return c
}

type surface struct{ h *host }

func (s surface) Display() contracts.DisplayID { return "shot" }
func (s surface) Present(_ context.Context, f *image.RGBA) error {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	if s.h.last == nil {
		s.h.last = image.NewRGBA(f.Bounds())
	}
	copy(s.h.last.Pix, f.Pix)
	return nil
}
func (surface) SetInteractive(contracts.Rect) error { return nil }
func (surface) Close() error                        { return nil }

// writeGIF animates the scene at 12.5 fps: the question fades in, then the
// hint plays its entrance and shimmer, then a pause before the loop. Only
// the changed area of each frame is stored.
func writeGIF(path string, app image.Image) error {
	const step = 80 * time.Millisecond
	const quiet, bubbleIn, hintAt, total = 4, 4, 10, 46 // frames
	hint, err := hintFrames(true, step, time.Duration(total-hintAt)*step)
	if err != nil {
		return err
	}
	k := float64(scene.gifW) / winW
	gw, gh := scene.gifW, int(math.Round(winH*k))
	pal := palette(app)
	out := &gif.GIF{LoopCount: 0}
	var prev *image.RGBA
	for i := range total {
		fr := image.NewRGBA(image.Rect(0, 0, winW, winH))
		draw.Draw(fr, fr.Bounds(), app, image.Point{}, draw.Src)
		if i >= quiet {
			a := math.Min(1, float64(i-quiet+1)/bubbleIn)
			bubble(fr, scene.bubble, a, 1)
		}
		if i >= hintAt {
			draw.Draw(fr, fr.Bounds(), hint[i-hintAt], image.Point{}, draw.Over)
		}
		small := image.NewRGBA(image.Rect(0, 0, gw, gh))
		xdraw.CatmullRom.Scale(small, small.Bounds(), fr, fr.Bounds(), draw.Src, nil)
		r := small.Bounds()
		if prev != nil {
			r = diff(prev, small)
			if r.Empty() {
				out.Delay[len(out.Delay)-1] += int(step / (10 * time.Millisecond))
				continue
			}
		}
		pm := image.NewPaletted(r, pal)
		draw.FloydSteinberg.Draw(pm, r, small, r.Min)
		out.Image = append(out.Image, pm)
		out.Delay = append(out.Delay, int(step/(10*time.Millisecond)))
		out.Disposal = append(out.Disposal, gif.DisposalNone)
		prev = small
	}
	// Hold the finished hint before starting over.
	out.Delay[len(out.Delay)-1] += 120
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := gif.EncodeAll(f, out); err != nil {
		f.Close()
		return err
	}
	fmt.Println(path)
	return f.Close()
}

// diff bounds the pixels that differ between two frames of one size.
func diff(a, b *image.RGBA) image.Rectangle {
	var r image.Rectangle
	w := a.Bounds().Dx()
	for y := range a.Bounds().Dy() {
		row := y * a.Stride
		for x := range w {
			o := row + x*4
			if a.Pix[o] != b.Pix[o] || a.Pix[o+1] != b.Pix[o+1] || a.Pix[o+2] != b.Pix[o+2] {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

// palette is the application's most used colours plus ramps from its
// greys to the brand violet and to white, so the ring's glow and the
// bubble do not band.
func palette(app image.Image) color.Palette {
	counts := map[[3]uint8]int{}
	b := app.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 2 {
		for x := b.Min.X; x < b.Max.X; x += 2 {
			r, g, bl, _ := app.At(x, y).RGBA()
			counts[[3]uint8{uint8(r>>8) &^ 7, uint8(g>>8) &^ 7, uint8(bl>>8) &^ 7}]++
		}
	}
	var pal color.Palette
	seen := map[[3]uint8]bool{}
	add := func(c color.NRGBA) {
		k := [3]uint8{c.R, c.G, c.B}
		if !seen[k] && len(pal) < 256 {
			seen[k] = true
			pal = append(pal, color.NRGBA{c.R, c.G, c.B, 255})
		}
	}
	violet, bub := color.NRGBA{0x8b, 0x5c, 0xf6, 255}, hsl(258, 32, 22)
	for _, from := range []color.NRGBA{rgb(0x303030), rgb(0x3d3d3d), rgb(0x1d1d1d), rgb(0x111111)} {
		for i := 1; i <= 12; i++ {
			add(lerp(from, violet, float64(i)/12))
		}
	}
	for i := 0; i <= 8; i++ {
		add(lerp(violet, rgb(0xffffff), float64(i)/8))
		add(lerp(bub, rgb(0xffffff), float64(i)/8))
		add(lerp(rgb(0x111111), rgb(0xffffff), float64(i)/8))
	}
	add(hsl(258, 40, 32))
	add(rgb(0xc4b5fd))
	for len(pal) < 256 && len(counts) > 0 {
		var best [3]uint8
		n := -1
		for k, v := range counts {
			if v > n || v == n && fmt.Sprint(k) < fmt.Sprint(best) {
				best, n = k, v
			}
		}
		delete(counts, best)
		add(color.NRGBA{best[0] + 4, best[1] + 4, best[2] + 4, 255})
	}
	return pal
}

// bubble draws the user's spoken question as banner B shows it: a violet
// chat bubble with a mic icon, its lower right corner tucked. a fades it.
func bubble(dst *image.RGBA, at image.Point, a, scale float64) {
	const px, padX, padY, icon, gap = 15.0, 14.0, 10.0, 14.0, 8.0
	f := newFace(sans, px*scale)
	tw := font.MeasureString(f, scene.question).Ceil()
	w := int((padX*2+icon+gap)*scale) + tw
	h := int((padY*2 + px*1.2) * scale)
	r := image.Rect(0, 0, w, h).Add(at)
	fade := func(c color.NRGBA, k float64) color.NRGBA { c.A = uint8(float64(c.A) * a * k); return c }

	sh := image.NewAlpha(dst.Bounds())
	fillRoundCorners(sh, r.Add(image.Pt(0, int(12*scale))), [4]float64{14, 14, 4, 14}, scale)
	blur(sh, int(10*scale))
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(fade(color.NRGBA{A: 255}, 0.45)), image.Point{}, sh, image.Point{}, draw.Over)
	edge := image.NewAlpha(dst.Bounds())
	fillRoundCorners(edge, r.Inset(-1), [4]float64{15, 15, 5, 15}, scale)
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(fade(hsl(258, 40, 32), 1)), image.Point{}, edge, image.Point{}, draw.Over)
	body := image.NewAlpha(dst.Bounds())
	fillRoundCorners(body, r, [4]float64{14, 14, 4, 14}, scale)
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(fade(hsl(258, 32, 22), 1)), image.Point{}, body, image.Point{}, draw.Over)

	mic(dst, float64(r.Min.X)+padX*scale, float64(r.Min.Y)+float64(h)/2-icon*scale/2, icon*scale, fade(rgb(0xc4b5fd), 1))
	asc, desc := f.Metrics().Ascent, f.Metrics().Descent
	base := fixed.I(r.Min.Y) + (fixed.I(h)-(asc+desc))/2 + asc
	d := font.Drawer{Dst: dst, Src: image.NewUniform(fade(rgb(0xfafafa), 1)), Face: f,
		Dot: fixed.Point26_6{X: fixed.I(r.Min.X + int((padX+icon+gap)*scale)), Y: base}}
	d.DrawString(scene.question)
}

// mic is MingCute's mic-line reduced to its shapes: a capsule outline, the
// stand's arc and its stem.
func mic(dst *image.RGBA, x, y, s float64, c color.NRGBA) {
	u := s / 24
	m := image.NewAlpha(dst.Bounds())
	fill := func(build func(z *vector.Rasterizer)) {
		z := vector.NewRasterizer(dst.Bounds().Dx(), dst.Bounds().Dy())
		build(z)
		z.Draw(m, m.Bounds(), image.Opaque, image.Point{})
	}
	capsule := func(z *vector.Rasterizer, x0, y0, x1, y1 float64, rev bool) {
		roundPathF(z, x+x0*u, y+y0*u, x+x1*u, y+y1*u, (x1-x0)*u/2, rev)
	}
	fill(func(z *vector.Rasterizer) {
		capsule(z, 7, 2, 17, 17, false)
		capsule(z, 9, 4, 15, 15, true)
	})
	fill(func(z *vector.Rasterizer) {
		// The arc: an annular half ring under the capsule.
		for i := 0; i <= 24; i++ {
			a := math.Pi * float64(i) / 24
			px, py := x+(12+7*math.Cos(a))*u, y+(12+7*math.Sin(a))*u
			if i == 0 {
				z.MoveTo(float32(px), float32(py))
			} else {
				z.LineTo(float32(px), float32(py))
			}
		}
		for i := 24; i >= 0; i-- {
			a := math.Pi * float64(i) / 24
			z.LineTo(float32(x+(12+5*math.Cos(a))*u), float32(y+(12+5*math.Sin(a))*u))
		}
		z.ClosePath()
		roundPathF(z, x+11*u, y+18*u, x+13*u, y+22*u, u, false)
	})
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(c), image.Point{}, m, image.Point{}, draw.Over)
}

var sans = func() *opentype.Font {
	f, err := opentype.Parse(fonts.Must("GoogleSans-Regular.ttf"))
	if err != nil {
		fail(err)
	}
	return f
}()

func newFace(f *opentype.Font, px float64) font.Face {
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		fail(err)
	}
	return fc
}

// fillRound sets a rounded rectangle in an alpha mask.
func fillRound(dst *image.Alpha, r image.Rectangle, rad float64, c color.Alpha) {
	z := vector.NewRasterizer(dst.Bounds().Dx(), dst.Bounds().Dy())
	roundPathF(z, float64(r.Min.X), float64(r.Min.Y), float64(r.Max.X), float64(r.Max.Y), rad, false)
	m := image.NewAlpha(dst.Bounds())
	z.Draw(m, m.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(c), image.Point{}, m, image.Point{}, draw.Over)
}

// fillRoundCorners is fillRound with its own radius per corner: top left,
// top right, bottom right, bottom left.
func fillRoundCorners(dst *image.Alpha, r image.Rectangle, rad [4]float64, scale float64) {
	z := vector.NewRasterizer(dst.Bounds().Dx(), dst.Bounds().Dy())
	x0, y0, x1, y1 := float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)
	var k [4]float32
	for i := range k {
		k[i] = float32(rad[i] * scale)
	}
	c := func(i int) float32 { return k[i] * (1 - 0.5523) }
	z.MoveTo(x0+k[0], y0)
	z.LineTo(x1-k[1], y0)
	z.CubeTo(x1-c(1), y0, x1, y0+c(1), x1, y0+k[1])
	z.LineTo(x1, y1-k[2])
	z.CubeTo(x1, y1-c(2), x1-c(2), y1, x1-k[2], y1)
	z.LineTo(x0+k[3], y1)
	z.CubeTo(x0+c(3), y1, x0, y1-c(3), x0, y1-k[3])
	z.LineTo(x0, y0+k[0])
	z.CubeTo(x0, y0+c(0), x0+c(0), y0, x0+k[0], y0)
	z.ClosePath()
	z.Draw(dst, dst.Bounds(), image.Opaque, image.Point{})
}

// roundPathF adds a rounded rectangle; rev winds it the other way so it
// cuts a hole out of an enclosing one.
func roundPathF(z *vector.Rasterizer, x0, y0, x1, y1, rad float64, rev bool) {
	k := rad
	// Control points 0.552·k along the tangents give a near-circular corner.
	c := k * (1 - 0.5523)
	pts := [][]float64{
		{x0 + k, y0}, {x1 - k, y0}, {x1 - c, y0, x1, y0 + c, x1, y0 + k},
		{x1, y1 - k}, {x1, y1 - c, x1 - c, y1, x1 - k, y1},
		{x0 + k, y1}, {x0 + c, y1, x0, y1 - c, x0, y1 - k},
		{x0, y0 + k}, {x0, y0 + c, x0 + c, y0, x0 + k, y0},
	}
	if rev {
		// The same outline walked backwards.
		z.MoveTo(float32(x0+k), float32(y0))
		z.CubeTo(float32(x0+c), float32(y0), float32(x0), float32(y0+c), float32(x0), float32(y0+k))
		z.LineTo(float32(x0), float32(y1-k))
		z.CubeTo(float32(x0), float32(y1-c), float32(x0+c), float32(y1), float32(x0+k), float32(y1))
		z.LineTo(float32(x1-k), float32(y1))
		z.CubeTo(float32(x1-c), float32(y1), float32(x1), float32(y1-c), float32(x1), float32(y1-k))
		z.LineTo(float32(x1), float32(y0+k))
		z.CubeTo(float32(x1), float32(y0+c), float32(x1-c), float32(y0), float32(x1-k), float32(y0))
		z.ClosePath()
		return
	}
	z.MoveTo(float32(pts[0][0]), float32(pts[0][1]))
	for _, p := range pts[1:] {
		if len(p) == 2 {
			z.LineTo(float32(p[0]), float32(p[1]))
		} else {
			z.CubeTo(float32(p[0]), float32(p[1]), float32(p[2]), float32(p[3]), float32(p[4]), float32(p[5]))
		}
	}
	z.ClosePath()
}

// blur approximates a Gaussian with three box passes each way.
func blur(m *image.Alpha, r int) {
	w, h := m.Bounds().Dx(), m.Bounds().Dy()
	buf := make([]float64, max(w, h))
	pass := func(get func(i int) float64, set func(i int, v float64), n int) {
		for i := range n {
			buf[i] = get(i)
		}
		var sum float64
		for i := -r; i <= r; i++ {
			sum += buf[min(max(i, 0), n-1)]
		}
		for i := range n {
			set(i, sum/float64(2*r+1))
			sum += buf[min(i+r+1, n-1)] - buf[max(i-r, 0)]
		}
	}
	for range 3 {
		for y := range h {
			row := m.Pix[y*m.Stride:]
			pass(func(i int) float64 { return float64(row[i]) }, func(i int, v float64) { row[i] = uint8(v) }, w)
		}
		for x := range w {
			pass(func(i int) float64 { return float64(m.Pix[i*m.Stride+x]) }, func(i int, v float64) { m.Pix[i*m.Stride+x] = uint8(v) }, h)
		}
	}
}

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

// hsl converts the app palette's HSL values (degrees, percent, percent).
func hsl(h, s, l float64) color.NRGBA {
	s, l = s/100, l/100
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	to := func(v float64) uint8 { return uint8(math.Round((v + m) * 255)) }
	return color.NRGBA{R: to(r), G: to(g), B: to(b), A: 255}
}

func lerp(p, q color.NRGBA, t float64) color.NRGBA {
	f := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return color.NRGBA{R: f(p.R, q.R), G: f(p.G, q.G), B: f(p.B, q.B), A: 255}
}

func mustRead(dir, name string) image.Image {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		fail(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fail(fmt.Errorf("%s: %w", name, err))
	}
	if b := img.Bounds(); b.Dx() != winW || b.Dy() != winH {
		fail(fmt.Errorf("%s: %dx%d, want %dx%d", name, b.Dx(), b.Dy(), winW, winH))
	}
	return img
}

func writePNG(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		fail(err)
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, img); err != nil {
		fail(err)
	}
	if err := f.Close(); err != nil {
		fail(err)
	}
	fmt.Println(path)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "genscreens:", err)
	os.Exit(1)
}
