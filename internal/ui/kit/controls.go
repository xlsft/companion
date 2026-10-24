package kit

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/theme"
)

// Form controls and small marks: discs, radio cards, tags, chips, the
// select box, segmented control, toggle switch and slider.

// Disc is a circle of d prototype pixels with w centred on it; a zero
// colour leaves the fill or the outline out.
func Disc(th *theme.Theme, d float32, bg, border color.NRGBA, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(th.Dp(d))
		gtx.Constraints = layout.Exact(image.Pt(s, s))
		if bg.A != 0 {
			paint.FillShape(gtx.Ops, bg, clip.Ellipse{Max: image.Pt(s, s)}.Op(gtx.Ops))
		}
		if border.A != 0 {
			paint.FillShape(gtx.Ops, border, clip.Stroke{Path: clip.Ellipse{Max: image.Pt(s, s)}.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
		}
		if w == nil {
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}
		return layout.Center.Layout(gtx, w)
	}
}

// digitHeight is the height of a lining digit as a fraction of the font
// size in the bundled sans; numbers are centred on it, not on the line box.
const digitHeight = 0.72

// NumberDisc is a disc with a number centred on it: the wizard's step
// marker. A label's box includes the descent below the baseline, so
// layout.Center would draw a digit above the middle; the number is placed
// by its baseline instead.
func NumberDisc(th *theme.Theme, d float32, num string, fg, bg, border color.NRGBA, bold bool) layout.Widget {
	return Disc(th, d, bg, border, func(gtx layout.Context) layout.Dimensions {
		s := gtx.Constraints.Max
		size := float32(11)
		l := label(th, size, num, fg)
		l.MaxLines = 1
		if bold {
			l.Font.Weight = font.Bold
		}
		gtx.Constraints.Min = image.Point{}
		m := op.Record(gtx.Ops)
		dims := l.Layout(gtx)
		call := m.Stop()
		digit := float32(gtx.Sp(px(th, size))) * digitHeight
		top := dims.Size.Y - dims.Baseline // line top to baseline
		y := int((float32(s.Y)+digit)/2+0.5) - top
		x := (s.X - dims.Size.X) / 2
		st := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		return layout.Dimensions{Size: s}
	})
}

// RadioMark is the 14px radio circle; selection shows as a filled dot,
// not by colour alone.
func RadioMark(th *theme.Theme, sel bool) layout.Widget {
	if !sel {
		return Disc(th, 14, color.NRGBA{}, th.Pal.Border, nil)
	}
	return Disc(th, 14, th.Pal.AccentLine, color.NRGBA{}, Disc(th, 6, th.Pal.AccentFg, color.NRGBA{}, nil))
}

// RadioCard is one option of a radio group drawn as the prototypes'
// selectable card: accent outline and tint when chosen. Its content is
// centred vertically, so cards stretched to a common height by Grid keep
// their mark level with the text.
func RadioCard(gtx layout.Context, th *theme.Theme, e *widget.Enum, key, title, sub string) layout.Dimensions {
	sel := e.Value == key
	hk, hovered := e.Hovered()
	fk, focused := e.Focused()
	border, bg := th.Pal.Divider, th.Pal.Surface
	if sel {
		border, bg = th.Pal.AccentLine, th.Pal.AccentTint
	} else if hovered && hk == key && gtx.Enabled() {
		bg = th.Pal.Hover
	}
	tfg, sfg := th.Pal.Fg, th.Pal.Muted
	if !gtx.Enabled() {
		tfg, sfg = th.Pal.Fg2, th.Pal.Subtle
	}
	return FocusRing(gtx, th, focused && fk == key, func(gtx layout.Context) layout.Dimensions {
		return e.Layout(gtx, key, func(gtx layout.Context) layout.Dimensions {
			semantic.SelectedOp(sel).Add(gtx.Ops)
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return Box(gtx, th, bg, border, 4, func(gtx layout.Context) layout.Dimensions {
				h := gtx.Constraints.Min.Y
				return VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: th.Dp(12), Bottom: th.Dp(12), Left: th.Dp(14), Right: th.Dp(14)}.Layout(gtx,
						func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(RadioMark(th, sel)),
								HSpace(th.Dp(10)),
								layout.Flexed(1, VStack(th, 2,
									Medium(label(th, 14, title, tfg)).Layout,
									label(th, 12, sub, sfg).Layout)),
							)
						})
				})
			})
		})
	})
}

// TagKind picks a Tag's look.
type TagKind int

const (
	// TagAccent is the prototypes' violet tag, e.g. the hardware an STT
	// model is recommended for.
	TagAccent TagKind = iota
	// TagNeutral marks something not available yet ("Soon").
	TagNeutral
)

// Tag is a small 22px label on a tint.
func Tag(gtx layout.Context, th *theme.Theme, txt string, kind TagKind) layout.Dimensions {
	bg, fg, border := th.Pal.AccentSoft, th.Pal.AccentText, color.NRGBA{}
	if kind == TagNeutral {
		bg, fg, border = th.Pal.Raised, th.Pal.Muted, th.Pal.Divider
	}
	h := gtx.Dp(th.Dp(20))
	return Box(gtx, th, bg, border, 4, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.Y = h
		return layout.Inset{Left: th.Dp(7), Right: th.Dp(7)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			l := label(th, 11, txt, fg)
			l.MaxLines = 1
			if kind == TagNeutral {
				l.Font.Weight = font.Medium
			}
			return VCenter(gtx, h, l.Layout)
		})
	})
}

// Chip is a 28px outlined label with a leading icon, e.g. the model name
// in a page header.
func Chip(gtx layout.Context, th *theme.Theme, n icon.Name, txt string) layout.Dimensions {
	h := gtx.Dp(th.Dp(28))
	return Box(gtx, th, color.NRGBA{}, th.Pal.Divider, 4, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.Y = h
		return layout.Inset{Left: th.Dp(10), Right: th.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(IconW(th, n, 12, th.Pal.Muted)),
				HSpace(th.Dp(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := label(th, 12.5, txt, th.Pal.Fg2)
					l.MaxLines = 1
					return VCenter(gtx, h, l.Layout)
				}),
			)
		})
	})
}

// Kbd shows a key chord as the prototypes' <kbd>: mono on a raised chip.
func Kbd(gtx layout.Context, th *theme.Theme, chord string) layout.Dimensions {
	return Box(gtx, th, th.Pal.Raised, th.Pal.Border, 4, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: th.Dp(2), Bottom: th.Dp(2), Left: th.Dp(6), Right: th.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			l := label(th, 11.5, chord, th.Pal.Fg)
			l.Font.Typeface = th.Mono()
			l.MaxLines = 1
			return l.Layout(gtx)
		})
	})
}

// Labeled puts a caption above a control, as the prototypes' <label>.
func Labeled(th *theme.Theme, caption string, w layout.Widget) layout.Widget {
	return VStack(th, 4, Caption(th, caption).Layout, w)
}

// Dropdown is the select box: a 36px box showing the current value that
// opens a list of options over the page; Gio has no native select. Esc or
// a click outside closes it, Up/Down move between options. UI goroutine
// only.
type Dropdown struct {
	// Up opens the list above the box, for a box at the bottom of a column.
	Up bool

	open      bool
	focusOpts bool // move focus into the list on the next frame
	toggle    widget.Clickable
	options   []widget.Clickable
	list      widget.List
	outside   int // pointer target covering the window while open
}

// Opened reports whether the option list is showing.
func (d *Dropdown) Opened() bool { return d.open }

// SetOpen shows or hides the option list, as a click on the box would.
func (d *Dropdown) SetOpen(open bool) { d.open = open }

// Pick opens the list and clicks option i, as the user would; the next
// Update returns it.
func (d *Dropdown) Pick(i int) {
	if i >= 0 && i < len(d.options) {
		d.open = true
		d.options[i].Click()
	}
}

// Update returns the index chosen since the last frame, or -1.
func (d *Dropdown) Update(gtx layout.Context, n int) int {
	if len(d.options) < n {
		d.options = append(d.options, make([]widget.Clickable, n-len(d.options))...)
	}
	if d.toggle.Clicked(gtx) {
		d.open = !d.open
		d.focusOpts = d.open
	}
	if !d.open {
		return -1
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &d.outside, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Kind == pointer.Press {
			d.close(gtx)
		}
	}
	for i := range n {
		if d.options[i].Clicked(gtx) {
			d.close(gtx)
			return i
		}
		for {
			ev, ok := gtx.Event(
				key.Filter{Focus: &d.options[i], Name: key.NameUpArrow},
				key.Filter{Focus: &d.options[i], Name: key.NameDownArrow},
				key.Filter{Focus: &d.options[i], Name: key.NameEscape},
			)
			if !ok {
				break
			}
			e, isKey := ev.(key.Event)
			if !isKey || e.State != key.Press {
				continue
			}
			switch e.Name {
			case key.NameEscape:
				d.close(gtx)
			case key.NameUpArrow:
				gtx.Execute(key.FocusCmd{Tag: &d.options[max(0, i-1)]})
			case key.NameDownArrow:
				gtx.Execute(key.FocusCmd{Tag: &d.options[min(n-1, i+1)]})
			}
		}
	}
	return -1
}

func (d *Dropdown) close(gtx layout.Context) {
	d.open = false
	gtx.Execute(key.FocusCmd{Tag: &d.toggle})
	gtx.Execute(op.InvalidateCmd{})
}

// Layout draws the box; labels[sel] is the current value, or placeholder
// when sel is out of range. A disabled context closes the list.
func (d *Dropdown) Layout(gtx layout.Context, th *theme.Theme, labels []string, sel int, placeholder string) layout.Dimensions {
	cur := placeholder
	if sel >= 0 && sel < len(labels) {
		cur = labels[sel]
	}
	gtx, fg := d.prepare(gtx, th, len(labels))
	dims := d.box(gtx, th, cur, 36, func(gtx layout.Context) layout.Dimensions {
		l := label(th, 14, cur, fg)
		l.MaxLines = 1
		return l.Layout(gtx)
	})
	if d.open {
		d.popup(gtx, th, len(labels), sel, dims.Size, false, func(gtx layout.Context, i int) layout.Dimensions {
			return d.option(gtx, th, i, labels[i], i == sel)
		})
	}
	return dims
}

// prepare closes the list of a disabled or empty select and sizes the
// option clickables; fg is the box's text colour.
func (d *Dropdown) prepare(gtx layout.Context, th *theme.Theme, n int) (layout.Context, color.NRGBA) {
	if !gtx.Enabled() || n == 0 {
		gtx = gtx.Disabled()
		d.open = false
	}
	if len(d.options) < n {
		d.options = append(d.options, make([]widget.Clickable, n-len(d.options))...)
	}
	if !gtx.Enabled() {
		return gtx, th.Pal.Subtle
	}
	return gtx, th.Pal.Fg
}

// box is the closed select: content on the left, the arrow on the right.
func (d *Dropdown) box(gtx layout.Context, th *theme.Theme, desc string, h float32, content layout.Widget) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return FocusRing(gtx, th, gtx.Focused(&d.toggle), func(gtx layout.Context) layout.Dimensions {
		return d.toggle.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.DescriptionOp(desc).Add(gtx.Ops)
			border, bg := th.Pal.Divider, th.Pal.Raised
			if d.open {
				border = th.Pal.AccentLine
			}
			if d.toggle.Hovered() && gtx.Enabled() {
				bg = th.Pal.Hover
			}
			return Box(gtx, th, bg, border, 4, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: th.Dp(12), Right: th.Dp(10)}.Layout(gtx, FixedH(th, h, func(gtx layout.Context) layout.Dimensions {
					arrow := icon.Down
					if d.open != d.Up {
						arrow = icon.Up
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, content),
						HSpace(th.Dp(10)),
						layout.Rigid(IconW(th, arrow, 14, th.Pal.Muted)),
					)
				}))
			})
		})
	})
}

// popup draws the option list over everything else in the window. Plain
// rows sit inside a 4px inset; rich rows run edge to edge with dividers.
func (d *Dropdown) popup(gtx layout.Context, th *theme.Theme, n, sel int, head image.Point, rich bool, row func(gtx layout.Context, i int) layout.Dimensions) {
	if d.focusOpts {
		d.focusOpts = false
		gtx.Execute(key.FocusCmd{Tag: &d.options[max(0, sel)]})
	}
	rowH, inset, rows := gtx.Dp(th.Dp(34)), th.Dp(4), 10
	if rich {
		rowH, inset, rows = gtx.Dp(th.Dp(richRowH)), 0, 6
	}
	h := min(n, rows)*rowH + 2*gtx.Dp(inset) + 2
	gtx.Constraints = layout.Exact(image.Pt(head.X, h))

	list := op.Record(gtx.Ops)
	Panel(gtx, th, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Exact(image.Pt(head.X, h))
		return layout.UniformInset(inset).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			d.list.Axis = layout.Vertical
			// The scrollbar floats over the rows so a long list keeps the
			// same padding as a short one.
			ls := material.List(th.Theme, &d.list)
			ls.AnchorStrategy = material.Overlay
			return ls.Layout(gtx, n, row)
		})
	})
	call := list.Stop()

	m := op.Record(gtx.Ops)
	// A window-wide catcher under the list closes it on an outside click.
	big := clip.Rect{Min: image.Pt(-1<<15, -1<<15), Max: image.Pt(1<<15, 1<<15)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &d.outside)
	big.Pop()
	gap := gtx.Dp(th.Dp(4))
	y := head.Y + gap
	if d.Up {
		y = -h - gap
	}
	st := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
	op.Defer(gtx.Ops, m.Stop())
}

func (d *Dropdown) option(gtx layout.Context, th *theme.Theme, i int, txt string, sel bool) layout.Dimensions {
	o := &d.options[i]
	return o.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.SelectedOp(sel).Add(gtx.Ops)
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		gtx.Constraints.Min.Y = gtx.Dp(th.Dp(34))
		bg := color.NRGBA{}
		if o.Hovered() || gtx.Focused(o) {
			bg = th.Pal.Hover
		}
		return Box(gtx, th, bg, color.NRGBA{}, 4, func(gtx layout.Context) layout.Dimensions {
			// Text lines up with the field's own text and the check with
			// its arrow: the popup's 4px inset plus 8px here make the
			// field's 12px. The chosen option carries a check, not only a
			// tint.
			var mark layout.Widget
			if sel {
				mark = IconW(th, icon.Check, 14, th.Pal.AccentText)
			}
			return layout.Inset{Left: th.Dp(8), Right: th.Dp(6)}.Layout(gtx, FixedH(th, 34, func(gtx layout.Context) layout.Dimensions {
				l := label(th, 14, txt, th.Pal.Fg)
				l.MaxLines = 1
				return Spread(l.Layout, mark)(gtx)
			}))
		})
	})
}

// Option is one segment of a Segmented control.
type Option struct {
	Key, Label string
}

// Segmented is the prototypes' segmented control for a short choice: the
// chosen segment is a raised, medium-weight chip.
func Segmented(gtx layout.Context, th *theme.Theme, e *widget.Enum, opts []Option) layout.Dimensions {
	fk, focused := e.Focused()
	hk, hovered := e.Hovered()
	return Box(gtx, th, th.Pal.Raised, th.Pal.Divider, 4, func(gtx layout.Context) layout.Dimensions {
		return layout.UniformInset(th.Dp(3)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			ch := make([]layout.FlexChild, 0, len(opts))
			for _, o := range opts {
				ch = append(ch, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					sel := e.Value == o.Key
					return FocusRing(gtx, th, focused && fk == o.Key, func(gtx layout.Context) layout.Dimensions {
						return e.Layout(gtx, o.Key, func(gtx layout.Context) layout.Dimensions {
							semantic.SelectedOp(sel).Add(gtx.Ops)
							bg, fg := color.NRGBA{}, th.Pal.Fg2
							switch {
							case !gtx.Enabled():
								fg = th.Pal.Subtle
								if sel {
									bg = th.Pal.Hover
								}
							case sel:
								bg, fg = th.Pal.Border, th.Pal.Fg
							case hovered && hk == o.Key:
								bg = th.Pal.Hover
							}
							return Box(gtx, th, bg, color.NRGBA{}, 3, func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Left: th.Dp(12), Right: th.Dp(12)}.Layout(gtx, FixedH(th, 30, func(gtx layout.Context) layout.Dimensions {
									l := label(th, 13, o.Label, fg)
									l.MaxLines = 1
									if sel {
										l.Font.Weight = font.Medium
									}
									return l.Layout(gtx)
								}))
							})
						})
					})
				}))
			}
			return layout.Flex{}.Layout(gtx, ch...)
		})
	})
}

// Toggle is the prototypes' 40×22 switch; on moves the knob to the right
// and fills the track, so the state is not shown by colour alone. desc is
// its accessible name.
func Toggle(gtx layout.Context, th *theme.Theme, b *widget.Bool, desc string) layout.Dimensions {
	return FocusRing(gtx, th, gtx.Focused(b), func(gtx layout.Context) layout.Dimensions {
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.DescriptionOp(desc).Add(gtx.Ops)
			w, h := gtx.Dp(th.Dp(40)), gtx.Dp(th.Dp(22))
			gtx.Constraints = layout.Exact(image.Pt(w, h))
			track, knob := th.Pal.Border, th.Pal.Fg2
			switch {
			case !gtx.Enabled() && b.Value:
				track, knob = th.Pal.AccentDisabled, th.Pal.FgDisabled
			case !gtx.Enabled():
				track, knob = th.Pal.Hover, th.Pal.Subtle
			case b.Value:
				track, knob = th.Pal.Accent, th.Pal.AccentFg
			case b.Hovered():
				track = th.Pal.Subtle
			}
			paint.FillShape(gtx.Ops, track, clip.UniformRRect(image.Rectangle{Max: image.Pt(w, h)}, h/2).Op(gtx.Ops))
			pad := gtx.Dp(th.Dp(2))
			k := h - 2*pad
			x := pad
			if b.Value {
				x = w - pad - k
			}
			paint.FillShape(gtx.Ops, knob, clip.Ellipse{Min: image.Pt(x, pad), Max: image.Pt(x+k, pad+k)}.Op(gtx.Ops))
			return layout.Dimensions{Size: image.Pt(w, h)}
		})
	})
}

// Slider is a horizontal value in [0, 1] with the prototypes' track and
// knob. It takes focus on click and Left/Right step it, so it works from
// the keyboard too. UI goroutine only.
type Slider struct {
	Float widget.Float
}

// Update reports a pointer change since the last frame and the number of
// arrow-key steps (negative for Left).
func (s *Slider) Update(gtx layout.Context) (changed bool, steps int) {
	changed = s.Float.Update(gtx)
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: s},
			key.Filter{Focus: s, Name: key.NameLeftArrow},
			key.Filter{Focus: s, Name: key.NameRightArrow},
			pointer.Filter{Target: s, Kinds: pointer.Press},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.Event:
			if e.State != key.Press {
				continue
			}
			if e.Name == key.NameLeftArrow {
				steps--
			} else {
				steps++
			}
		case pointer.Event:
			gtx.Execute(key.FocusCmd{Tag: s})
		}
	}
	return changed, steps
}

// Layout draws the slider across the available width.
func (s *Slider) Layout(gtx layout.Context, th *theme.Theme, desc string) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Dp(th.Dp(24))
	size := image.Pt(w, h)
	focused := gtx.Focused(s)
	// The focus target encloses the drag area so both see the press.
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	semantic.DescriptionOp(desc).Add(gtx.Ops)
	if gtx.Enabled() {
		event.Op(gtx.Ops, s)
	}

	knob := gtx.Dp(th.Dp(16))
	// The knob stays inside the track's ends.
	inner := w - knob
	track, fill, kfill, kborder := th.Pal.Hover, th.Pal.AccentLine, th.Pal.AccentFg, th.Pal.AccentLine
	if !gtx.Enabled() {
		fill, kfill, kborder = th.Pal.Border, th.Pal.Raised, th.Pal.Border
	}
	th4 := gtx.Dp(th.Dp(4))
	ty := (h - th4) / 2
	x := knob/2 + int(float32(inner)*s.Float.Value)
	paint.FillShape(gtx.Ops, track, clip.UniformRRect(image.Rect(knob/2, ty, w-knob/2, ty+th4), th4/2).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(knob/2, ty, x, ty+th4), th4/2).Op(gtx.Ops))
	kr := image.Rect(x-knob/2, (h-knob)/2, x+knob/2, (h+knob)/2)
	if focused {
		ring := gtx.Dp(th.Dp(3))
		paint.FillShape(gtx.Ops, th.Pal.AccentSoft, clip.Ellipse{Min: kr.Min.Sub(image.Pt(ring, ring)), Max: kr.Max.Add(image.Pt(ring, ring))}.Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, kfill, clip.Ellipse(kr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, kborder, clip.Stroke{Path: clip.Ellipse(kr).Path(gtx.Ops), Width: float32(max(1, gtx.Dp(th.Dp(2))))}.Op())

	if gtx.Enabled() {
		// widget.Float maps its minimum width to [0, 1]; the knob's half
		// widths are left out at both ends so the value follows the knob.
		st := op.Offset(image.Pt(knob/2, 0)).Push(gtx.Ops)
		fg := gtx
		fg.Constraints = layout.Exact(image.Pt(inner, h))
		s.Float.Layout(fg, layout.Horizontal, unit.Dp(float32(knob/2)/gtx.Metric.PxPerDp))
		st.Pop()
	}
	return layout.Dimensions{Size: size}
}
