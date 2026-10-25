package kit

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/theme"
)

// PillLead is the mark at a pill's start.
type PillLead int

const (
	LeadDot     PillLead = iota // something new waits
	LeadRing                    // progress, Pill.Progress in [0,1]; < 0 spins nowhere, a full track
	LeadCheck                   // done
	LeadWarning                 // failed
)

// Pill is the sidebar's compact status chip (the updater): a mark, a short
// text, an optional muted note at the end and an optional action button.
// Its tone follows the lead: accent for news, neutral while busy, OK when
// done, error when failed.
type Pill struct {
	Lead     PillLead
	Progress float32
	Text     string
	Meta     string // e.g. "19/38 MB"; shown when there is no action
	Action   string // button label; "" for none
	// InfoLabel names the info button for screen readers (e.g. "What's
	// new"); the button shows only when Layout gets a clickable for it.
	InfoLabel string
}

func (p Pill) colors(th *theme.Theme) (bg, border, mark color.NRGBA) {
	switch p.Lead {
	case LeadDot:
		return th.Pal.AccentSoft, th.Pal.AccentStrong, th.Pal.AccentText
	case LeadCheck:
		return th.Pal.OKSoft, th.Pal.Divider, th.Pal.OK
	case LeadWarning:
		return th.Pal.ErrSoft, th.Pal.Divider, th.Pal.Error
	}
	return th.Pal.Raised, th.Pal.Border, th.Pal.AccentLine
}

// Layout draws the pill across the available width. action is the button
// at its end, info a round ghost button after it (e.g. release notes);
// either may be nil. The pill itself is not a button: a click on action
// must not also count as one on the pill.
func (p Pill) Layout(gtx layout.Context, th *theme.Theme, action, info *widget.Clickable) layout.Dimensions {
	bg, border, mark := p.colors(th)
	h := gtx.Dp(th.Dp(34))
	content := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
		return Box(gtx, th, bg, border, 17, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: th.Dp(12), Right: th.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				var tail []layout.FlexChild
				if info != nil {
					tail = append(tail, HSpace(th.Dp(2)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return p.info(gtx, th, info) })
					}))
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, append([]layout.FlexChild{
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return p.lead(gtx, th, mark) })
					}),
					HSpace(th.Dp(8)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						t := Text(th, 13, p.Text, th.Pal.Fg)
						t.MaxLines = 1
						d := VCenter(gtx, h, t.Layout)
						d.Size.X = gtx.Constraints.Max.X
						return d
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if p.Action != "" && action != nil {
							return VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return p.button(gtx, th, action) })
						}
						if p.Meta == "" {
							return layout.Dimensions{}
						}
						t := Text(th, 12, p.Meta, th.Pal.Subtle)
						t.Font.Typeface = th.Mono()
						t.MaxLines = 1
						return layout.Inset{Right: th.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return VCenter(gtx, h, t.Layout) })
					}),
				}, tail...)...)
			})
		})
	}
	return content(gtx)
}

// info is the 22px round ghost button with the info icon; the hover
// circle is drawn from the square's own pixels, so it stays round at any
// scale.
func (p Pill) info(gtx layout.Context, th *theme.Theme, c *widget.Clickable) layout.Dimensions {
	var bg color.NRGBA
	col := th.Pal.Fg2
	if c.Hovered() {
		bg, col = mix(th.Pal.Raised, th.Pal.Fg, 0.1), th.Pal.Fg
	}
	s := gtx.Dp(th.Dp(22))
	return FocusRing(gtx, th, gtx.Focused(c), func(gtx layout.Context) layout.Dimensions {
		return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.DescriptionOp(p.InfoLabel).Add(gtx.Ops)
			gtx.Constraints = layout.Exact(image.Pt(s, s))
			if bg.A != 0 {
				paint.FillShape(gtx.Ops, bg, clip.Ellipse{Max: image.Pt(s, s)}.Op(gtx.Ops))
			}
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return Icon(gtx, th, icon.Information, 14, col)
			})
		})
	})
}

// button is the pill's 22px rounded action.
func (p Pill) button(gtx layout.Context, th *theme.Theme, c *widget.Clickable) layout.Dimensions {
	bg, fg := th.Pal.Accent, th.Pal.AccentFg
	if p.Lead == LeadCheck {
		bg, fg = th.Pal.OK, th.Pal.Bg
	}
	if c.Hovered() {
		bg = mix(bg, th.Pal.Fg, 0.12)
	}
	return FocusRing(gtx, th, gtx.Focused(c), func(gtx layout.Context) layout.Dimensions {
		return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.DescriptionOp(p.Action).Add(gtx.Ops)
			bh := gtx.Dp(th.Dp(22))
			gtx.Constraints.Min.Y = bh
			return Box(gtx, th, bg, color.NRGBA{}, 11, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: th.Dp(10), Right: th.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					t := Text(th, 12, p.Action, fg)
					t.Font.Weight = font.Medium
					t.MaxLines = 1
					return VCenter(gtx, bh, t.Layout)
				})
			})
		})
	})
}

func (p Pill) lead(gtx layout.Context, th *theme.Theme, col color.NRGBA) layout.Dimensions {
	switch p.Lead {
	case LeadDot:
		return Disc(th, 8, col, color.NRGBA{}, nil)(gtx)
	case LeadCheck:
		return Icon(gtx, th, icon.CheckCircle, 14, col)
	case LeadWarning:
		return Icon(gtx, th, icon.Warning, 14, col)
	}
	return ring(gtx, th, p.Progress, th.Pal.Divider, col)
}

// ring is an 18dp progress circle: a full track and an arc from the top
// clockwise over frac of it.
func ring(gtx layout.Context, th *theme.Theme, frac float32, track, col color.NRGBA) layout.Dimensions {
	s := gtx.Dp(th.Dp(18))
	w := float32(max(2, gtx.Dp(th.Dp(2))))
	c := float32(s) / 2
	r := c - w/2
	paint.FillShape(gtx.Ops, track, clip.Stroke{Path: clip.Ellipse{Min: image.Pt(int(w/2), int(w/2)), Max: image.Pt(s-int(w/2), s-int(w/2))}.Path(gtx.Ops), Width: w}.Op())
	if frac > 0 {
		frac = min(frac, 1)
		var pa clip.Path
		pa.Begin(gtx.Ops)
		// Clockwise from the top, drawn as short segments: ArcTo's sign
		// for a circle (both foci in the centre) is not the screen's.
		end := 2 * math.Pi * float64(frac)
		n := max(2, int(48*frac))
		pa.MoveTo(f32.Pt(c, c-r))
		for i := 1; i <= n; i++ {
			t := end * float64(i) / float64(n)
			pa.LineTo(f32.Pt(c+r*float32(math.Sin(t)), c-r*float32(math.Cos(t))))
		}
		paint.FillShape(gtx.Ops, col, clip.Stroke{Path: pa.End(), Width: w}.Op())
	}
	return layout.Dimensions{Size: image.Pt(s, s)}
}

// mix is src at alpha a over dst, like the theme's tints.
func mix(dst, src color.NRGBA, a float32) color.NRGBA {
	m := func(d, s uint8) uint8 { return uint8(float32(d)*(1-a) + float32(s)*a + 0.5) }
	return color.NRGBA{R: m(dst.R, src.R), G: m(dst.G, src.G), B: m(dst.B, src.B), A: 0xff}
}
