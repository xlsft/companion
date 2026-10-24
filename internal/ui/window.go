package ui

import (
	"image"
	"image/color"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/xlsft/openclick/internal/ui/theme"
)

// surface is a window's content.
type surface interface {
	Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions
}

// window is what the shell needs from a top-level window. All methods are
// asynchronous and safe from any goroutine, including the window's own.
type window interface {
	Invalidate()
	Raise()
	Close()
}

type windowSpec struct {
	Title         string
	Width, Height unit.Dp
	Content       surface
	Shell         *Shell
	// OnClosed runs on the window goroutine once the window is destroyed.
	OnClosed func(window)
}

// windowFactory is swapped in tests so shell logic runs without a display.
type windowFactory func(windowSpec) window

type gioWindow struct{ w *app.Window }

func newGioWindow(spec windowSpec) window {
	g := &gioWindow{w: new(app.Window)}
	g.w.Option(app.Title(spec.Title), app.Size(spec.Width, spec.Height), app.MinSize(480, 360))
	go g.loop(spec)
	return g
}

func (g *gioWindow) loop(spec windowSpec) {
	_, dark, scale := spec.Shell.look()
	th := theme.New(dark, scale)
	var ops op.Ops
	for {
		switch e := g.w.Event().(type) {
		case app.DestroyEvent:
			if e.Err != nil {
				spec.Shell.log.Error("window failed", "err", e.Err)
			}
			spec.OnClosed(g)
			return
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			renderFrame(gtx, th, spec.Shell, spec.Content)
			e.Frame(gtx.Ops)
		}
	}
}

func (g *gioWindow) Invalidate() { g.w.Invalidate() }

// Perform waits for the window's event loop, which may itself be waiting
// for the shell lock or be the caller; run it detached to avoid deadlock.
func (g *gioWindow) Raise() { go g.w.Perform(system.ActionRaise) }
func (g *gioWindow) Close() { go g.w.Perform(system.ActionClose) }

// renderFrame lays out one frame: background, global shortcuts, content.
func renderFrame(gtx layout.Context, th *theme.Theme, sh *Shell, content surface) layout.Dimensions {
	lang, dark, scale := sh.look()
	th.Apply(dark, scale)
	fill(gtx, th.Pal.Bg, gtx.Constraints.Max)
	handleShortcuts(gtx, sh)
	return content.Layout(gtx, th, lang)
}

// shortcutTarget is what global shortcuts act on.
type shortcutTarget interface {
	Quit()
	stepScale(n int)
	navShortcut(digit int)
}

var shortcutFilters = func() []event.Filter {
	fs := []event.Filter{
		key.Filter{Name: "Q", Required: key.ModShortcut},
		key.Filter{Name: "+", Required: key.ModShortcut, Optional: key.ModShift},
		key.Filter{Name: "=", Required: key.ModShortcut, Optional: key.ModShift},
		key.Filter{Name: "-", Required: key.ModShortcut},
		key.Filter{Name: "0", Required: key.ModShortcut},
	}
	for i := range Routes {
		fs = append(fs, key.Filter{Name: key.Name(rune('1' + i)), Required: key.ModShortcut})
	}
	return fs
}()

// handleShortcuts processes window-wide key chords. Key filters without a
// Focus tag are global in Gio, so these work whatever has focus.
func handleShortcuts(gtx layout.Context, t shortcutTarget) {
	for {
		ev, ok := gtx.Event(shortcutFilters...)
		if !ok {
			return
		}
		e, isKey := ev.(key.Event)
		if !isKey || e.State != key.Press {
			continue
		}
		switch e.Name {
		case "Q":
			// Quit cancels the app context; Stop then closes this window.
			go t.Quit()
		case "+", "=":
			t.stepScale(1)
		case "-":
			t.stepScale(-1)
		case "0":
			t.stepScale(0)
		default:
			if len(e.Name) == 1 && e.Name[0] >= '1' && e.Name[0] <= '9' {
				t.navShortcut(int(e.Name[0] - '0'))
			}
		}
	}
}

func fill(gtx layout.Context, col color.NRGBA, size image.Point) {
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.ColorOp{Color: col}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}
