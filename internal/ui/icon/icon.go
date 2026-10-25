// Package icon draws MingCute icons (Apache-2.0, see LICENSE.mingcute)
// and a few service logos from Simple Icons (CC0, LICENSE.simple-icons)
// as Gio vector paths. Gio has no SVG support, so the icons' path data is
// parsed once and filled with clip.Outline.
//
// Refresh mingcute.json with `go run gen.go icons.json` from an
// @iconify-json/mingcute package after adding a name there.
package icon

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"sync"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// Name is a MingCute icon id.
type Name string

const (
	Add         Name = "add-line"
	Aiming2     Name = "aiming-2-line"
	Alert       Name = "alert-line"
	ArrowRight  Name = "arrow-right-line"
	ArrowUp     Name = "arrow-up-line"
	Attachment  Name = "attachment-line"
	Brain       Name = "brain-line"
	Chat1       Name = "chat-1-line"
	CheckCircle Name = "check-circle-line"
	Check       Name = "check-line"
	CloseCircle Name = "close-circle-line"
	Close       Name = "close-line"
	Computer    Name = "computer-line"
	Copy2       Name = "copy-2-line"
	Dashboard2  Name = "dashboard-2-line"
	Delete2     Name = "delete-2-line"
	Down        Name = "down-line"
	Download2   Name = "download-2-line"
	Edit        Name = "edit-line"
	Exit        Name = "exit-line"
	EyeClose    Name = "eye-close-line"
	Eye         Name = "eye-line"
	Heartbeat   Name = "heartbeat-line"
	History     Name = "history-line"
	Information Name = "information-line"
	Key2        Name = "key-2-line"
	Keyboard    Name = "keyboard-line"
	Layers      Name = "layers-line"
	Left        Name = "left-line"
	ListCheck   Name = "list-check-line"
	Loading3    Name = "loading-3-line"
	Lock        Name = "lock-line"
	MicOff      Name = "mic-off-line"
	Mic         Name = "mic-line"
	More2       Name = "more-2-line"
	PauseFill   Name = "pause-fill"
	Pin         Name = "pin-line"
	PlayFill    Name = "play-fill"
	Refresh2    Name = "refresh-2-line"
	Right       Name = "right-line"
	Robot       Name = "robot-line"
	Search      Name = "search-line"
	Settings3   Name = "settings-3-line"
	Sparkles    Name = "sparkles-line"
	StopFill    Name = "stop-fill"
	Stop        Name = "stop-line"
	Time        Name = "time-line"
	Translate   Name = "translate-line"
	Unlink      Name = "unlink-line"
	Up          Name = "up-line"
	Voice       Name = "voice-line"
	Volume      Name = "volume-line"
	Warning     Name = "warning-line"
)

//go:embed mingcute.json
var data []byte

// brands are the service logos (Simple Icons), kept apart from the MingCute
// set so gen.go can rewrite mingcute.json without them.
//
//go:embed brands.json
var brands []byte

// Service logos for the setup's provider tiles.
const (
	BrandAnthropic  Name = "brand-anthropic"
	BrandOpenAI     Name = "brand-openai"
	BrandGemini     Name = "brand-gemini"
	BrandOpenRouter Name = "brand-openrouter"
	BrandMistral    Name = "brand-mistral"
	BrandOllama     Name = "brand-ollama"
	BrandElevenLabs Name = "brand-elevenlabs"
)

var (
	loadOnce sync.Once
	icons    map[Name][]contour
	loadErr  error
)

func load() {
	var raw, logos map[Name][]string
	if loadErr = json.Unmarshal(data, &raw); loadErr != nil {
		return
	}
	if loadErr = json.Unmarshal(brands, &logos); loadErr != nil {
		return
	}
	for n, p := range logos {
		raw[n] = p
	}
	icons = make(map[Name][]contour, len(raw))
	for n, paths := range raw {
		var cs []contour
		for _, d := range paths {
			c, err := parsePath(d)
			if err != nil {
				loadErr = fmt.Errorf("icon %s: %w", n, err)
				return
			}
			cs = append(cs, c...)
		}
		icons[n] = nonzero(cs)
	}
}

func contours(n Name) []contour {
	loadOnce.Do(load)
	if loadErr != nil {
		panic(loadErr) // embedded data; covered by tests
	}
	c, ok := icons[n]
	if !ok {
		panic("icon: unknown " + string(n))
	}
	return c
}

// Layout fills icon n in a size×size box.
func Layout(gtx layout.Context, n Name, size unit.Dp, col color.NRGBA) layout.Dimensions {
	px := gtx.Dp(size)
	s := float32(px) / 24
	var p clip.Path
	p.Begin(gtx.Ops)
	for _, c := range contours(n) {
		p.MoveTo(c[0].p0.Mul(s))
		for _, sg := range c {
			p.CubeTo(sg.c1.Mul(s), sg.c2.Mul(s), sg.p1.Mul(s))
		}
		p.Close()
	}
	paint.FillShape(gtx.Ops, col, clip.Outline{Path: p.End()}.Op())
	return layout.Dimensions{Size: image.Pt(px, px)}
}

// nonzero orients contours so Gio's non-zero fill draws what the SVG's
// even-odd rule meant: a contour nested inside an odd number of others is
// a hole and must wind against its parent.
func nonzero(cs []contour) []contour {
	polys := make([][]f32.Point, len(cs))
	for i, c := range cs {
		polys[i] = flatten(c)
	}
	out := make([]contour, len(cs))
	for i, c := range cs {
		depth := 0
		for j, q := range polys {
			if j != i && inside(q, polys[i][0]) {
				depth++
			}
		}
		positive := area(polys[i]) > 0
		if positive != (depth%2 == 0) {
			c = reverse(c)
		}
		out[i] = c
	}
	return out
}

func flatten(c contour) []f32.Point {
	pts := make([]f32.Point, 0, len(c)*8)
	for _, s := range c {
		for k := range 8 {
			pts = append(pts, s.at(float32(k)/8))
		}
	}
	return pts
}

func (s seg) at(t float32) f32.Point {
	u := 1 - t
	return s.p0.Mul(u * u * u).Add(s.c1.Mul(3 * u * u * t)).Add(s.c2.Mul(3 * u * t * t)).Add(s.p1.Mul(t * t * t))
}

func area(p []f32.Point) float32 {
	var a float32
	for i := range p {
		q := p[(i+1)%len(p)]
		a += p[i].X*q.Y - q.X*p[i].Y
	}
	return a / 2
}

// inside is the even-odd point-in-polygon test.
func inside(poly []f32.Point, pt f32.Point) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a.Y > pt.Y) != (b.Y > pt.Y) && pt.X < (b.X-a.X)*(pt.Y-a.Y)/(b.Y-a.Y)+a.X {
			in = !in
		}
	}
	return in
}

func reverse(c contour) contour {
	r := make(contour, len(c))
	for i, s := range c {
		r[len(c)-1-i] = seg{s.p1, s.c2, s.c1, s.p0}
	}
	return r
}
