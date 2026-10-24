//go:build ignore

// genshots composes the README screenshots: each raw window capture (taken
// from the spikes, see docs/development.md) is set on one calm backdrop with
// rounded corners and a soft shadow, and the "shows you where to click"
// shot is drawn here: a neutral editor window with the real overlay hint
// rendered over it by the overlay engine.
//
//	go run packaging/genshots.go -raw DIR -out docs/assets
//
// DIR holds chat-dark.png, guide-light.png and speech-dark.png, each a
// 1180×760 window.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/xlsft/companion/internal/assets/fonts"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/pkg/contracts"
)

const (
	canvasW, canvasH = 1600, 1000
	winW, winH       = 1180, 760
	radius           = 12.0
)

func main() {
	raw := flag.String("raw", "", "directory with the raw window captures")
	out := flag.String("out", "docs/assets", "output directory")
	flag.Parse()
	for _, name := range []string{"chat-dark", "guide-light", "speech-dark"} {
		win, err := readPNG(filepath.Join(*raw, name+".png"))
		if err != nil {
			fail(err)
		}
		write(filepath.Join(*out, "shot-"+name+".png"), frame(win))
	}
	win, err := pointing()
	if err != nil {
		fail(err)
	}
	write(filepath.Join(*out, "shot-pointing.png"), frame(win))
}

// frame centres a window on the backdrop with a shadow under it.
func frame(win image.Image) *image.RGBA {
	dst := backdrop()
	b := win.Bounds()
	x0, y0 := (canvasW-b.Dx())/2, (canvasH-b.Dy())/2-10
	r := image.Rect(x0, y0, x0+b.Dx(), y0+b.Dy())

	// The shadow is the window's shape, blurred and dropped.
	sh := image.NewAlpha(dst.Bounds())
	fillRound(sh, r.Add(image.Pt(0, 28)).Inset(-6), radius, color.Alpha{A: 255})
	blur(sh, 36)
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(color.NRGBA{A: 150}), image.Point{}, sh, image.Point{}, draw.Over)

	mask := image.NewAlpha(dst.Bounds())
	fillRound(mask, r, radius, color.Alpha{A: 255})
	draw.DrawMask(dst, r, win, b.Min, mask, r.Min, draw.Over)
	// A hairline keeps light and dark windows apart from the backdrop.
	ring := image.NewAlpha(dst.Bounds())
	fillRound(ring, r.Inset(-1), radius+1, color.Alpha{A: 255})
	fillRound(ring, r, radius, color.Alpha{})
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(color.NRGBA{R: 255, G: 255, B: 255, A: 30}), image.Point{}, ring, image.Point{}, draw.Over)
	return dst
}

// backdrop is a dark zinc field with a violet glow from the top.
func backdrop() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	base := [3]float64{0x0c, 0x0c, 0x0f}
	glow := [3]float64{0x3b, 0x2a, 0x6e}
	for y := range canvasH {
		for x := range canvasW {
			dx, dy := (float64(x)-canvasW*0.5)/(canvasW*0.75), (float64(y)+canvasH*0.15)/(canvasH*1.25)
			t := math.Max(0, 1-math.Hypot(dx, dy))
			t = t * t * (3 - 2*t) * 0.75
			// Ordered dither keeps the gradient from banding once quantised.
			n := (bayer[y%4][x%4] - 7.5) / 16
			var c [3]uint8
			for i := range 3 {
				c[i] = uint8(math.Max(0, math.Min(255, base[i]+(glow[i]-base[i])*t+n)))
			}
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = c[0], c[1], c[2], 255
		}
	}
	return img
}

var bayer = [4][4]float64{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}

// fillRound sets a rounded rectangle in an alpha mask.
func fillRound(dst *image.Alpha, r image.Rectangle, rad float64, c color.Alpha) {
	z := vector.NewRasterizer(dst.Bounds().Dx(), dst.Bounds().Dy())
	roundPath(z, r, rad)
	m := image.NewAlpha(dst.Bounds())
	z.Draw(m, m.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(c), image.Point{}, m, image.Point{}, draw.Src)
}

func roundPath(z *vector.Rasterizer, r image.Rectangle, rad float64) {
	x0, y0, x1, y1 := float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)
	k := float32(rad)
	// Control points 0.552·k along the tangents give a near-circular corner.
	c := k * (1 - 0.5523)
	z.MoveTo(x0+k, y0)
	z.LineTo(x1-k, y0)
	z.CubeTo(x1-c, y0, x1, y0+c, x1, y0+k)
	z.LineTo(x1, y1-k)
	z.CubeTo(x1, y1-c, x1-c, y1, x1-k, y1)
	z.LineTo(x0+k, y1)
	z.CubeTo(x0+c, y1, x0, y1-c, x0, y1-k)
	z.LineTo(x0, y0+k)
	z.CubeTo(x0, y0+c, x0+c, y0, x0+k, y0)
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

// pointing draws a neutral, light design editor and the overlay's point_at
// hint on its Export button, as the model would request it.
func pointing() (*image.RGBA, error) {
	win := editor()
	btn := exportButton
	cx, cy := float64(btn.Min.X+btn.Max.X)/2/winW, float64(btn.Min.Y+btn.Max.Y)/2/winH
	hint, err := hintFrame(fmt.Sprintf(`{"x":%.4f,"y":%.4f,"radius":0.042,"label":"Click here"}`, cx, cy))
	if err != nil {
		return nil, err
	}
	draw.Draw(win, win.Bounds(), hint, image.Point{}, draw.Over)
	return win, nil
}

// host keeps the last frame the engine presents instead of showing it.
type host struct{ last *image.RGBA }

func (h *host) Open(context.Context, contracts.DisplayID) (contracts.OverlaySurface, error) {
	return surface{h}, nil
}

type surface struct{ h *host }

func (s surface) Display() contracts.DisplayID { return "mock" }
func (s surface) Present(_ context.Context, f *image.RGBA) error {
	s.h.last = image.NewRGBA(f.Bounds())
	draw.Draw(s.h.last, f.Bounds(), f, image.Point{}, draw.Src)
	return nil
}
func (surface) SetInteractive(contracts.Rect) error { return nil }
func (surface) Close() error                        { return nil }

func hintFrame(args string) (*image.RGBA, error) {
	ctx := context.Background()
	d := contracts.Display{ID: "mock", Native: contracts.PixelSize{W: winW, H: winH}, Bounds: contracts.RectXYWH(0, 0, winW, winH), Scale: 1, Primary: true}
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{d}}
	h := &host{}
	e := overlay.NewEngine(h, func(context.Context) (contracts.Topology, error) { return topo, nil })
	e.SetMotion(false)
	meta := contracts.CaptureMetadata{ID: "shot", DisplayID: d.ID, Native: d.Native, Bounds: d.Bounds, Scale: 1, TopologyVersion: 1, Timestamp: time.Now()}
	e.RegisterCapture(meta)
	cc := overlay.CallContext{Session: contracts.SessionRef{ID: "shot", Generation: 1}, Capture: meta.ID, CallID: "point", Segment: -1}
	cmds, err := overlay.FromToolCall(cc, overlay.ToolPointAt, json.RawMessage(args))
	if err != nil {
		return nil, err
	}
	for _, c := range cmds {
		if err := e.Apply(ctx, c); err != nil {
			return nil, err
		}
	}
	if h.last == nil {
		return nil, fmt.Errorf("overlay presented nothing")
	}
	return h.last, nil
}

// The editor's palette: light zinc, like a generic design tool.
var (
	cBg      = rgb(0xf4f4f5)
	cPanel   = rgb(0xffffff)
	cLine    = rgb(0xe4e4e7)
	cInk     = rgb(0x18181b)
	cMuted   = rgb(0x71717a)
	cField   = rgb(0xf4f4f5)
	cSelect  = rgb(0xede9fe)
	cPrimary = rgb(0x18181b)
)

// exportButton is compact so the ring encloses it and the label fits
// beside it inside the window.
var exportButton = image.Rect(winW-244, 604, winW-152, 640)

func editor() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, winW, winH))
	fill(img, img.Bounds(), cBg)
	sans, md := face("GoogleSans-Regular.ttf"), face("GoogleSans-Medium.ttf")
	txt := func(f *opentype.Font, s string, px float64, x, y int, c color.NRGBA) int {
		return text(img, f, s, px, x, y, c)
	}

	// Title bar.
	fill(img, image.Rect(0, 0, winW, 44), cPanel)
	fill(img, image.Rect(0, 44, winW, 45), cLine)
	for i := range 3 {
		disc(img, 22+float64(i)*20, 22, 6, rgb(0xd4d4d8))
	}
	w := measure(md, "Summer poster", 14)
	txt(md, "Summer poster", 14, (winW-w)/2, 27, cInk)

	// Layers.
	fill(img, image.Rect(0, 45, 232, winH), cPanel)
	fill(img, image.Rect(232, 45, 233, winH), cLine)
	txt(md, "Layers", 13, 20, 76, cInk)
	for i, l := range []string{"Poster", "Headline", "Subtitle", "Photo", "Background"} {
		y := 96 + i*34
		x := 20
		if i > 0 {
			x = 40
		}
		if i == 0 {
			round(img, image.Rect(10, y-2, 222, y+28), 6, cSelect)
		}
		box(img, image.Rect(x, y+7, x+12, y+19), cMuted)
		txt(sans, l, 13, x+22, y+18, cInk)
	}

	// Canvas with the artboard.
	art := image.Rect(330, 110, 330+500, 110+560)
	shadowRect(img, art)
	fill(img, art, cPanel)
	photo := image.Rect(art.Min.X+36, art.Min.Y+36, art.Max.X-36, art.Min.Y+340)
	gradient(img, photo, rgb(0xfde68a), rgb(0xf472b6), rgb(0x8b5cf6))
	disc(img, float64(photo.Max.X-90), float64(photo.Min.Y+80), 42, color.NRGBA{R: 255, G: 255, B: 255, A: 190})
	txt(md, "Summer Fest", 44, art.Min.X+36, art.Min.Y+410, cInk)
	txt(sans, "Live music · Food · Friends", 18, art.Min.X+36, art.Min.Y+446, cMuted)
	txt(md, "July 12", 18, art.Min.X+36, art.Min.Y+510, rgb(0x7c3aed))
	txt(sans, "Poster", 12, art.Min.X, art.Min.Y-10, cMuted)

	// Properties.
	px0 := winW - 264
	fill(img, image.Rect(px0, 45, winW, winH), cPanel)
	fill(img, image.Rect(px0, 45, px0+1, winH), cLine)
	section := func(title string, y int) {
		fill(img, image.Rect(px0, y-26, winW, y-25), cLine)
		txt(md, title, 13, px0+20, y, cInk)
	}
	field := func(r image.Rectangle, s string) {
		round(img, r, 6, cField)
		txt(sans, s, 13, r.Min.X+10, r.Min.Y+(r.Dy()+9)/2, cInk)
	}
	txt(md, "Design", 13, px0+20, 76, cInk)
	txt(sans, "Prototype", 13, px0+84, 76, cMuted)
	section("Frame", 124)
	field(image.Rect(px0+20, 140, px0+118, 172), "W  500")
	field(image.Rect(px0+126, 140, winW-20, 172), "H  560")
	section("Fill", 222)
	field(image.Rect(px0+20, 238, winW-20, 270), "       FFFFFF        100%")
	round(img, image.Rect(px0+28, 246, px0+44, 262), 4, rgb(0xffffff))
	box(img, image.Rect(px0+28, 246, px0+44, 262), cLine)
	section("Stroke", 320)
	txt(sans, "No stroke", 13, px0+20, 346, cMuted)
	section("Effects", 396)
	txt(sans, "Drop shadow", 13, px0+20, 422, cMuted)
	section("Export", 528)
	field(image.Rect(px0+20, 548, px0+90, 580), "2x")
	field(image.Rect(px0+98, 548, winW-20, 580), "PNG")
	round(img, exportButton, 7, cPrimary)
	label := "Export"
	lw := measure(md, label, 13)
	txt(md, label, 13, exportButton.Min.X+(exportButton.Dx()-lw)/2, exportButton.Min.Y+24, rgb(0xffffff))
	return img
}

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

func fill(img *image.RGBA, r image.Rectangle, c color.NRGBA) {
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Over)
}

func round(img *image.RGBA, r image.Rectangle, rad float64, c color.NRGBA) {
	z := vector.NewRasterizer(img.Bounds().Dx(), img.Bounds().Dy())
	roundPath(z, r, rad)
	z.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{})
}

func box(img *image.RGBA, r image.Rectangle, c color.NRGBA) {
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+1), c)
	fill(img, image.Rect(r.Min.X, r.Max.Y-1, r.Max.X, r.Max.Y), c)
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+1, r.Max.Y), c)
	fill(img, image.Rect(r.Max.X-1, r.Min.Y, r.Max.X, r.Max.Y), c)
}

func disc(img *image.RGBA, cx, cy, r float64, c color.NRGBA) {
	z := vector.NewRasterizer(img.Bounds().Dx(), img.Bounds().Dy())
	k := float32(r * 0.5523)
	x, y, rr := float32(cx), float32(cy), float32(r)
	z.MoveTo(x+rr, y)
	z.CubeTo(x+rr, y+k, x+k, y+rr, x, y+rr)
	z.CubeTo(x-k, y+rr, x-rr, y+k, x-rr, y)
	z.CubeTo(x-rr, y-k, x-k, y-rr, x, y-rr)
	z.CubeTo(x+k, y-rr, x+rr, y-k, x+rr, y)
	z.ClosePath()
	z.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{})
}

func shadowRect(img *image.RGBA, r image.Rectangle) {
	m := image.NewAlpha(img.Bounds())
	draw.Draw(m, r.Add(image.Pt(0, 6)), image.NewUniform(color.Alpha{A: 255}), image.Point{}, draw.Src)
	blur(m, 10)
	draw.DrawMask(img, img.Bounds(), image.NewUniform(color.NRGBA{A: 40}), image.Point{}, m, image.Point{}, draw.Over)
}

// gradient fills r diagonally through three colours.
func gradient(img *image.RGBA, r image.Rectangle, a, b, c color.NRGBA) {
	lerp := func(p, q color.NRGBA, t float64) color.NRGBA {
		f := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
		return color.NRGBA{R: f(p.R, q.R), G: f(p.G, q.G), B: f(p.B, q.B), A: 255}
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			t := (float64(x-r.Min.X)/float64(r.Dx()) + float64(y-r.Min.Y)/float64(r.Dy())) / 2
			col := lerp(a, b, t*2)
			if t > 0.5 {
				col = lerp(b, c, (t-0.5)*2)
			}
			img.Set(x, y, col)
		}
	}
}

func face(name string) *opentype.Font {
	f, err := opentype.Parse(fonts.Must(name))
	if err != nil {
		fail(err)
	}
	return f
}

func newFace(f *opentype.Font, px float64) font.Face {
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		fail(err)
	}
	return fc
}

func measure(f *opentype.Font, s string, px float64) int {
	return font.MeasureString(newFace(f, px), s).Ceil()
}

// text draws s with its baseline at y and returns its width.
func text(img *image.RGBA, f *opentype.Font, s string, px float64, x, y int, c color.NRGBA) int {
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: newFace(f, px), Dot: fixed.P(x, y)}
	d.DrawString(s)
	return (d.Dot.X - fixed.I(x)).Ceil()
}

func readPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if b := img.Bounds(); b.Dx() != winW || b.Dy() != winH {
		return nil, fmt.Errorf("%s: %dx%d, want %dx%d", path, b.Dx(), b.Dy(), winW, winH)
	}
	return img, nil
}

func write(path string, img image.Image) {
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
	fmt.Fprintln(os.Stderr, "genshots:", err)
	os.Exit(1)
}
