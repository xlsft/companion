package ui

import (
	"image"
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/ui/wizard"
)

// page is one main-window section.
type page interface {
	Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions
}

// mainUI is the main window content. It exists only in ModeMain and
// survives window close/re-open so widget state is kept.
type mainUI struct {
	sh *Shell

	// mu guards nav, pages and section; it is a leaf lock (never held
	// while calling into the shell) because the tray goroutine navigates
	// too.
	mu    sync.Mutex
	nav   Navigator
	pages map[Route]page
	// section is a Settings section to show on the next frame, or -1.
	section settingsSection

	chat    *chatModel
	guide   *guideModel
	update  *updateChip
	dictate *dictationBanner

	newChatBtn widget.Clickable
	aboutBtn   widget.Clickable
	navBtns    map[Route]*widget.Clickable
	about      dialog
}

func newMainUI(sh *Shell) *mainUI {
	m := &mainUI{
		sh:      sh,
		pages:   map[Route]page{},
		chat:    newChatModel(sh.opts.Chat, sh.log, sh.invalidateAll),
		guide:   newGuideModel(sh.opts.Guide, sh.log, sh.invalidateAll),
		update:  newUpdateChip(sh.opts.Updates),
		dictate: newDictationBanner(sh.opts.Dictation),
		navBtns: map[Route]*widget.Clickable{},
		section: -1,
	}
	for _, r := range NavRoutes() {
		m.navBtns[r] = new(widget.Clickable)
	}
	return m
}

func (m *mainUI) navigate(r Route) {
	m.mu.Lock()
	m.nav.Go(r)
	m.mu.Unlock()
}

// showSettings opens Settings at section s; safe from any goroutine (the
// tray's Diagnostics item uses it).
func (m *mainUI) showSettings(s settingsSection) {
	m.mu.Lock()
	m.nav.Go(RouteSettings)
	m.section = s
	m.mu.Unlock()
}

// takeSection returns a pending section switch, if any.
func (m *mainUI) takeSection() (settingsSection, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.section
	m.section = -1
	return s, s >= 0
}

func (m *mainUI) current() Route {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nav.Current()
}

// newChat starts an empty conversation and shows it.
func (m *mainUI) newChat() {
	m.chat.newChat()
	m.navigate(RouteHome)
}

func (m *mainUI) close() {
	m.chat.close()
	m.guide.close()
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
	case RouteSettings:
		sp := newSettingsPage(m.sh.opts.Settings)
		sp.updates = m.sh.opts.Updates
		sp.diag = newDiagView(m.sh.diag, m.sh.opts.Version)
		sp.jump = m.takeSection
		sp.changeVoice = m.sh.opts.ChangeVoice
		if be := m.sh.opts.Memory; be != nil {
			sp.mem = newMemView(be, m.sh.invalidateAll)
		}
		if be := m.sh.opts.Setup.Permissions; be != nil {
			sp.hotkeys = &wizard.HotkeyEditor{Backend: be, Invalidate: m.sh.invalidateAll}
			if d, ok := be.(wizard.DictateHotkeys); ok {
				sp.dictate = &wizard.DictateEditor{Backend: d, Invalidate: m.sh.invalidateAll}
			}
		}
		if be := m.sh.opts.Setup.STT; be != nil {
			sp.models = &wizard.ModelPicker{Backend: be, Invalidate: m.sh.invalidateAll, Language: func() string {
				s := m.sh.opts.Settings.Get()
				return hw.SpeechLanguage(s.Voice.Language, s.UI.Language)
			}}
		}
		p = sp
	default:
		// Only implemented routes are ever current.
		p = newHomePage(m)
	}
	m.pages[r] = p
	return p
}

func (m *mainUI) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	for r, b := range m.navBtns {
		if b.Clicked(gtx) {
			m.navigate(r)
		}
	}
	if m.newChatBtn.Clicked(gtx) {
		m.newChat()
	}
	if m.aboutBtn.Clicked(gtx) {
		m.about.showNote(T(l, "about.title"), Tf(l, "about.body", m.sh.opts.Version), T(l, "nav.hint"))
	}
	m.chat.ensure()
	cur := m.current()
	pg := m.page(cur)
	return layout.Stack{}.Layout(gtx,
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return m.dictate.wrap(gtx, th, l, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return m.sidebar(gtx, th, l, cur) }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.VDivider(gtx, th) }),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return pg.Layout(gtx, th, l) }),
				)
			})
		}),
		layout.Expanded(func(gtx layout.Context) layout.Dimensions { return m.about.Layout(gtx, th, l) }),
	)
}

// sidebarWidth is the prototypes' sidebar column in CSS pixels.
const sidebarWidth = 260

func (m *mainUI) sidebar(gtx layout.Context, th *theme.Theme, l Lang, cur Route) layout.Dimensions {
	w := gtx.Dp(th.Dp(sidebarWidth))
	gtx.Constraints = layout.Exact(image.Pt(w, gtx.Constraints.Max.Y))
	fill(gtx, th.Pal.Sidebar, gtx.Constraints.Max)
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return m.logoRow(gtx, th, l) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Divider(gtx, th) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if m.chat.be == nil {
				gtx = gtx.Disabled()
			}
			return layout.Inset{Top: th.Dp(12), Bottom: th.Dp(4), Left: th.Dp(8), Right: th.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return newChatButton(gtx, th, &m.newChatBtn, T(l, "sidebar.newChat"))
			})
		}),
		kit.VSpace(th.Dp(8)),
	}
	for _, r := range Routes {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: th.Dp(8), Right: th.Dp(8), Bottom: th.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				if !r.Implemented() {
					return kit.NavItemSoon(gtx, th, r.icon(), T(l, r.labelKey()), T(l, "nav.soon"))
				}
				return kit.NavItem(gtx, th, m.navBtns[r], r.icon(), T(l, r.labelKey()), r == cur)
			})
		}))
	}
	children = append(children,
		layout.Flexed(1, layout.Spacer{}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Divider(gtx, th) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return sidebarStatus(gtx, th, l, m.chat) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return m.update.Layout(gtx, th, l) }),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// logoRow is the 54px brand row; it lines up with the page headers.
func (m *mainUI) logoRow(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	h := gtx.Dp(th.Dp(54))
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	return layout.Inset{Left: th.Dp(20), Right: th.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				d := kit.VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return kit.LogoVersion(gtx, th, 15, m.sh.opts.Version) })
				d.Size.X = gtx.Constraints.Max.X
				return d
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions {
					return kit.IconButton(gtx, th, &m.aboutBtn, icon.Information, T(l, "home.about"))
				})
			}),
		)
	})
}

// newChatButton is the sidebar's full-width "New chat" with its shortcut.
func newChatButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, label string) layout.Dimensions {
	fg, bg := th.Pal.Fg, th.Pal.Raised
	switch {
	case !gtx.Enabled():
		fg = th.Pal.Subtle
	case c.Hovered():
		bg = th.Pal.Hover
	}
	return kit.FocusRing(gtx, th, gtx.Focused(c), func(gtx layout.Context) layout.Dimensions {
		return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			h := gtx.Dp(th.Dp(36))
			gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
			return kit.Box(gtx, th, bg, th.Pal.Border, 4, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: th.Dp(12), Right: th.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return kit.VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return kit.Icon(gtx, th, icon.Add, 16, fg) })
						}),
						kit.HSpace(th.Dp(8)),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							t := kit.Text(th, 14, label, fg)
							t.MaxLines = 1
							return kit.VCenter(gtx, h, t.Layout)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							t := kit.Text(th, 11, "Ctrl+N", th.Pal.Subtle)
							t.Font.Typeface = th.Mono()
							return kit.VCenter(gtx, h, t.Layout)
						}),
					)
				})
			})
		})
	})
}
