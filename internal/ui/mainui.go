package ui

import (
	"image"
	"sync"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/openclick/internal/ui/theme"
)

// page is one main-window section.
type page interface {
	Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions
}

// mainUI is the main window content. It exists only in ModeMain and
// survives window close/re-open so widget state is kept.
type mainUI struct {
	sh *Shell

	// mu guards nav and pages; it is a leaf lock (never held while calling
	// into the shell) because the tray goroutine navigates too.
	mu    sync.Mutex
	nav   Navigator
	pages map[Route]page

	navBtns []widget.Clickable
	about   dialog
}

func newMainUI(sh *Shell) *mainUI {
	return &mainUI{sh: sh, pages: map[Route]page{}, navBtns: make([]widget.Clickable, len(Routes))}
}

func (m *mainUI) navigate(r Route) {
	m.mu.Lock()
	m.nav.Go(r)
	m.mu.Unlock()
}

func (m *mainUI) current() Route {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nav.Current()
}

// page builds a section on first visit, so unvisited sections cost nothing.
func (m *mainUI) page(r Route) page {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pages[r]; ok {
		return p
	}
	var p page
	switch r {
	case RouteHome:
		p = newHomePage(m)
	case RouteSettings:
		p = newSettingsPage(m.sh.opts.Settings)
	case RouteDiagnostics:
		p = newDiagPage(m.sh.diag)
	default:
		p = placeholderPage{route: r}
	}
	m.pages[r] = p
	return p
}

func (m *mainUI) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	for i := range m.navBtns {
		if m.navBtns[i].Clicked(gtx) {
			m.navigate(Routes[i])
		}
	}
	cur := m.current()
	pg := m.page(cur)
	return layout.Stack{}.Layout(gtx,
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = gtx.Constraints.Max
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return m.sidebar(gtx, th, l, cur) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					fill(gtx, th.Pal.Divider, image.Pt(gtx.Dp(1), gtx.Constraints.Max.Y))
					return layout.Dimensions{Size: image.Pt(gtx.Dp(1), gtx.Constraints.Max.Y)}
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return pg.Layout(gtx, th, l) }),
			)
		}),
		layout.Expanded(func(gtx layout.Context) layout.Dimensions { return m.about.Layout(gtx, th, l) }),
	)
}

func (m *mainUI) sidebar(gtx layout.Context, th *theme.Theme, l Lang, cur Route) layout.Dimensions {
	w := gtx.Dp(unit.Dp(210 * th.Scale))
	gtx.Constraints = layout.Exact(image.Pt(w, gtx.Constraints.Max.Y))
	fill(gtx, th.Pal.Sidebar, gtx.Constraints.Max)
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 16, Right: 16, Top: 20, Bottom: 16}.Layout(gtx, subheading(th, T(l, "app.name")).Layout)
		}),
	}
	for i, r := range Routes {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return navItem(gtx, th, &m.navBtns[i], T(l, r.labelKey()), r == cur)
		}))
	}
	children = append(children,
		layout.Flexed(1, layout.Spacer{}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(16).Layout(gtx, muted(th, T(l, "nav.hint")).Layout)
		}),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func navItem(gtx layout.Context, th *theme.Theme, btn *widget.Clickable, label string, selected bool) layout.Dimensions {
	return layout.Inset{Left: 8, Right: 8, Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return focusRing(gtx, th, gtx.Focused(btn), func(gtx layout.Context) layout.Dimensions {
			return material.Clickable(gtx, btn, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Background{}.Layout(gtx,
					func(gtx layout.Context) layout.Dimensions {
						if selected {
							fill(gtx, th.Pal.Selected, gtx.Constraints.Min)
						}
						return layout.Dimensions{Size: gtx.Constraints.Min}
					},
					func(gtx layout.Context) layout.Dimensions {
						prefix := "   "
						if selected {
							prefix = theme.SelectedMarker + " "
						}
						lbl := body(th, prefix+label)
						if selected {
							lbl.Font.Weight = font.Bold
						}
						return layout.Inset{Top: 8, Bottom: 8, Left: 8, Right: 8}.Layout(gtx, lbl.Layout)
					})
			})
		})
	})
}

// homePage is the main landing section; Talk and chats attach here later.
type homePage struct {
	m           *mainUI
	settingsBtn widget.Clickable
	aboutBtn    widget.Clickable
}

func newHomePage(m *mainUI) *homePage { return &homePage{m: m} }

func (p *homePage) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	if p.settingsBtn.Clicked(gtx) {
		p.m.navigate(RouteSettings)
	}
	if p.aboutBtn.Clicked(gtx) {
		p.m.about.show(T(l, "about.title"), Tf(l, "about.body", p.m.sh.opts.Version))
	}
	return layout.UniformInset(24).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(heading(th, T(l, "home.heading")).Layout),
			vspace(8),
			layout.Rigid(th.StatusLabel(theme.StatusOK, T(l, "home.status")).Layout),
			vspace(12),
			layout.Rigid(body(th, T(l, "home.body")).Layout),
			vspace(20),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return button(gtx, th, &p.settingsBtn, T(l, "home.settings"))
					}),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return button(gtx, th, &p.aboutBtn, T(l, "home.about"))
					}),
				)
			}),
		)
	})
}
