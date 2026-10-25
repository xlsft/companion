package ui

import (
	"context"
	"math"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/ui/wizard"
)

// TTS speed range mirrors settings.Validate; the slider snaps to sliderSnap
// and arrow keys move it by buttonStep.
const (
	speedMin   = 0.5
	speedMax   = 1.5
	sliderSnap = 0.05
	buttonStep = 0.1
	saveTimout = 2 * time.Second
)

func clampSpeed(v float64) float64 {
	v = math.Round(v/sliderSnap) * sliderSnap
	// Re-round to two decimals so repeated steps don't drift (0.1 is not exact).
	v = math.Round(v*100) / 100
	return math.Min(speedMax, math.Max(speedMin, v))
}

func speedFromSlider(v float32) float64 { return clampSpeed(speedMin + float64(v)*(speedMax-speedMin)) }

func sliderFromSpeed(s float64) float32 { return float32((s - speedMin) / (speedMax - speedMin)) }

func stepSpeed(cur float64, dir int) float64 { return clampSpeed(cur + float64(dir)*buttonStep) }

// settingsSection is a group of Settings, shown one at a time.
type settingsSection int

const (
	secGeneral settingsSection = iota
	secVoice
	secScreen
	secPrivacy
	secMemory
	secDiagnostics
)

var settingsSections = []settingsSection{secGeneral, secVoice, secScreen, secPrivacy, secMemory, secDiagnostics}

func (s settingsSection) key() string {
	switch s {
	case secVoice:
		return "settings.sec.voice"
	case secScreen:
		return "settings.sec.screen"
	case secPrivacy:
		return "settings.sec.privacy"
	case secMemory:
		return "settings.sec.memory"
	case secDiagnostics:
		return "settings.sec.diagnostics"
	}
	return "settings.sec.general"
}

func (s settingsSection) icon() icon.Name {
	switch s {
	case secVoice:
		return icon.Mic
	case secScreen:
		return icon.Computer
	case secPrivacy:
		return icon.Lock
	case secMemory:
		return icon.Sparkles
	case secDiagnostics:
		return icon.Heartbeat
	}
	return icon.Settings3
}

// sectionByName finds a section by its key suffix, e.g. "diagnostics".
func sectionByName(name string) (settingsSection, bool) {
	for _, s := range settingsSections {
		if s.key() == "settings.sec."+name {
			return s, true
		}
	}
	return 0, false
}

// settingsPage renders FR-UI-004 bound to the settings service, one
// section at a time after Settings.dc.html. The widgets are a view: every
// frame they are reset from the service, so a rejected update snaps back
// to the stored value. Settings the app does not act on yet are shown
// disabled with a "Soon" tag, never as if they worked.
type settingsPage struct {
	store SettingsStore
	list  widget.List
	diag  *diagView
	// jump returns a section switch requested from outside (the tray).
	jump func() (settingsSection, bool)

	section settingsSection
	secBtns [6]widget.Clickable
	// secDD stands in for the section list in a narrow page.
	secDD kit.Dropdown

	hotkey, screenTarget widget.Enum
	uiLang               kit.Dropdown

	speed        kit.Slider
	speedPending bool

	keepShots, memory, saveText, shareDiag, login, tray, dictClip widget.Bool

	// updates is the updater behind the General section's update rows;
	// nil leaves "Check now" out.
	updates         UpdateBackend
	autoUpd, preUpd widget.Bool
	checkNow        widget.Clickable

	// changeVoice leads to the voice choice; nil hides the button.
	changeVoice func()
	voiceBtn    widget.Clickable

	saveErr bool

	// mem lists the remembered facts; nil leaves the section with only its
	// switch.
	mem *memView

	// hotkeys edits the Talk and Cancel shortcuts; nil without a backend.
	hotkeys *wizard.HotkeyEditor
	// models picks the speech model; nil without an STT backend.
	models *wizard.ModelPicker
	// dictate edits the dictation shortcut; nil without a backend.
	dictate *wizard.DictateEditor
}

func newSettingsPage(store SettingsStore) *settingsPage {
	p := &settingsPage{store: store}
	p.list.Axis = layout.Vertical
	return p
}

func (p *settingsPage) apply(fn func(*settings.Settings)) {
	ctx, cancel := context.WithTimeout(context.Background(), saveTimout)
	defer cancel()
	p.saveErr = p.store.Update(ctx, fn) != nil
}

// update turns widget events into settings writes.
func (p *settingsPage) update(gtx layout.Context) {
	if p.jump != nil {
		if s, ok := p.jump(); ok {
			p.section = s
			p.list.Position = layout.Position{}
		}
	}
	for i := range p.secBtns {
		if p.secBtns[i].Clicked(gtx) && p.section != settingsSections[i] {
			p.section = settingsSections[i]
			p.list.Position = layout.Position{}
		}
	}
	if i := p.secDD.Update(gtx, len(settingsSections)); i >= 0 {
		p.section = settingsSections[i]
		p.list.Position = layout.Position{}
	}
	if p.hotkey.Update(gtx) {
		v := settings.HotkeyMode(p.hotkey.Value)
		p.apply(func(s *settings.Settings) { s.Voice.HotkeyMode = v })
	}
	if p.screenTarget.Update(gtx) {
		v := settings.ScreenTarget(p.screenTarget.Value)
		p.apply(func(s *settings.Settings) { s.Screen.Target = v })
	}
	if i := p.uiLang.Update(gtx, len(i18n.Languages)); i >= 0 {
		v := string(i18n.Languages[i].Code)
		p.apply(func(s *settings.Settings) { s.UI.Language = v })
	}
	for _, b := range []struct {
		w   *widget.Bool
		set func(*settings.Settings, bool)
	}{
		{&p.keepShots, func(s *settings.Settings, v bool) { s.Screen.KeepScreenshots = v }},
		{&p.memory, func(s *settings.Settings, v bool) { s.Memory.Enabled = v }},
		{&p.saveText, func(s *settings.Settings, v bool) { s.Privacy.SaveTranscripts = v }},
		{&p.shareDiag, func(s *settings.Settings, v bool) { s.Privacy.ShareDiagnostics = v }},
		{&p.login, func(s *settings.Settings, v bool) { s.Platform.LaunchAtLogin = v }},
		{&p.tray, func(s *settings.Settings, v bool) { s.Platform.TrayIcon = v }},
		{&p.autoUpd, func(s *settings.Settings, v bool) { s.Updates.AutoCheck = v }},
		{&p.preUpd, func(s *settings.Settings, v bool) { s.Updates.Prereleases = v }},
		{&p.dictClip, func(s *settings.Settings, v bool) { s.Voice.DictationClipboard = v }},
	} {
		if b.w.Update(gtx) {
			v, set := b.w.Value, b.set
			p.apply(func(s *settings.Settings) { set(s, v) })
		}
	}
	if p.voiceBtn.Clicked(gtx) && p.changeVoice != nil {
		p.changeVoice()
	}
	if p.checkNow.Clicked(gtx) && p.updates != nil {
		p.updates.CheckNow()
	}
	moved, steps := p.speed.Update(gtx)
	if moved {
		p.speedPending = true
	}
	// Persist a drag once it ends, not on every motion event.
	if p.speedPending && !p.speed.Float.Dragging() {
		p.speedPending = false
		v := speedFromSlider(p.speed.Float.Value)
		p.apply(func(s *settings.Settings) { s.Voice.TTSSpeed = v })
	}
	if steps != 0 {
		p.apply(func(s *settings.Settings) { s.Voice.TTSSpeed = stepSpeed(s.Voice.TTSSpeed, steps) })
	}
}

// sync resets the widgets from the stored settings.
func (p *settingsPage) sync(s settings.Settings) {
	p.hotkey.Value = string(s.Voice.HotkeyMode)
	p.screenTarget.Value = string(s.Screen.Target)
	p.keepShots.Value = s.Screen.KeepScreenshots
	p.memory.Value = s.Memory.Enabled
	p.saveText.Value = s.Privacy.SaveTranscripts
	p.shareDiag.Value = s.Privacy.ShareDiagnostics
	p.login.Value = s.Platform.LaunchAtLogin
	p.tray.Value = s.Platform.TrayIcon
	p.autoUpd.Value = s.Updates.AutoCheck
	p.preUpd.Value = s.Updates.Prereleases
	p.dictClip.Value = s.Voice.DictationClipboard
	if !p.speed.Float.Dragging() && !p.speedPending {
		p.speed.Float.Value = sliderFromSpeed(s.Voice.TTSSpeed)
	}
}

func (p *settingsPage) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	p.update(gtx)
	cur := p.store.Get()
	p.sync(cur)

	var actions layout.Widget
	var rows []layout.Widget
	if p.section == secDiagnostics && p.diag != nil {
		s := p.diag.update(gtx)
		actions = p.diag.actions(th, l, s)
		rows = p.diag.rows(th, l, s)
	} else {
		rows = p.sectionRows(gtx, th, l, cur)
	}
	title := T(l, p.section.key())
	rows = append([]layout.Widget{kit.Title(th, title).Layout}, rows...)
	if p.saveErr {
		rows = append(rows[:1], append([]layout.Widget{func(gtx layout.Context) layout.Dimensions {
			return kit.Alert(gtx, th, theme.StatusError, T(l, "settings.saveError"), T(l, "settings.saveError.d"))
		}}, rows[1:]...)...)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kit.PageHeader(gtx, th, kit.Breadcrumb(th, T(l, "settings.heading"), title), actions)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			// A narrow page (small window, large text) has no room for the
			// section list beside the panels; a select takes its place.
			if gtx.Constraints.Max.X < gtx.Dp(th.Dp(196+520)) {
				return p.content(gtx, th, append([]layout.Widget{p.sectionPicker(th, l)}, rows...))
			}
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.subnav(gtx, th, l) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.VDivider(gtx, th) }),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return p.content(gtx, th, rows) }),
			)
		}),
	)
}

// subnav lists the sections; it lines up with the main sidebar.
func (p *settingsPage) subnav(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	w := gtx.Dp(th.Dp(196))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
	items := make([]layout.Widget, len(settingsSections))
	for i, s := range settingsSections {
		items[i] = func(gtx layout.Context) layout.Dimensions {
			return kit.NavItem(gtx, th, &p.secBtns[i], s.icon(), T(l, s.key()), s == p.section)
		}
	}
	return layout.Inset{Top: th.Dp(12), Left: th.Dp(8), Right: th.Dp(8)}.Layout(gtx, kit.VStack(th, 2, items...))
}

// sectionPicker is the section list as a select.
func (p *settingsPage) sectionPicker(th *theme.Theme, l Lang) layout.Widget {
	names := make([]string, len(settingsSections))
	sel := 0
	for i, s := range settingsSections {
		names[i] = T(l, s.key())
		if s == p.section {
			sel = i
		}
	}
	return func(gtx layout.Context) layout.Dimensions {
		return p.secDD.Layout(gtx, th, names, sel, names[sel])
	}
}

// content is the section's scrolling column; ordinary sections keep the
// prototype's reading width, Diagnostics uses the whole page for its
// table.
func (p *settingsPage) content(gtx layout.Context, th *theme.Theme, rows []layout.Widget) layout.Dimensions {
	maxW := float32(760)
	if p.section == secDiagnostics {
		maxW = 1100
	}
	return material.List(th.Theme, &p.list).Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		side := kit.PageMargin(gtx, th, 32)
		in := layout.Inset{Top: th.Dp(16), Left: side, Right: side}
		if i == 0 {
			in.Top = th.Dp(28)
		}
		if i == len(rows)-1 {
			in.Bottom = th.Dp(28)
		}
		return in.Layout(gtx, kit.MaxWidth(th, maxW, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return rows[i](gtx)
		}))
	})
}

// setting is one row of a section panel.
func setting(th *theme.Theme, l Lang, title, desc string, ready bool, ctl layout.Widget) layout.Widget {
	r := kit.SettingRow{Title: T(l, title), Control: ctl}
	if desc != "" {
		r.Desc = T(l, desc)
	}
	if !ready {
		r.Soon = T(l, "nav.soon")
	}
	return func(gtx layout.Context) layout.Dimensions { return r.Layout(gtx, th) }
}

// voiceName is the speech provider in the user's words: a known service by
// its name, anything else as the user's own server.
func voiceName(l Lang, c settings.TTS) string {
	if c.ProviderID == "" {
		return T(l, "tts.none")
	}
	for _, pr := range tts.Presets {
		if pr.ID == c.ProviderID && (c.BaseURL == "" || strings.EqualFold(strings.TrimRight(c.BaseURL, "/"), pr.BaseURL)) {
			return pr.Name
		}
	}
	if pr, ok := tts.PresetFor(c.ProviderID, c.BaseURL); ok {
		return pr.Name
	}
	return T(l, "tts.custom")
}

func panelOf(th *theme.Theme, rows ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return kit.Rows(gtx, th, rows...) }
}

// sectionRows are a section's panels. ready marks what the app acts on
// today; the rest is disabled and tagged.
func (p *settingsPage) sectionRows(gtx layout.Context, th *theme.Theme, l Lang, cur settings.Settings) []layout.Widget {
	toggle := func(b *widget.Bool, desc string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions { return kit.Toggle(gtx, th, b, T(l, desc)) }
	}
	switch p.section {
	case secVoice:
		lang := T(l, "voice.languageAuto")
		if cur.Voice.Language != "auto" {
			lang = cur.Voice.Language
		}
		speed := cur.Voice.TTSSpeed
		if p.speedPending || p.speed.Float.Dragging() {
			speed = speedFromSlider(p.speed.Float.Value)
		}
		var shortcuts []layout.Widget
		if p.models != nil {
			shortcuts = append(shortcuts, panelOf(th,
				setting(th, l, "stt.model", "stt.model.d", true, nil),
				kit.Pad(th, 16, 20, p.models.Layout(th, l, nil)),
			))
		}
		if p.hotkeys != nil {
			shortcuts = append(shortcuts, panelOf(th,
				setting(th, l, "voice.shortcuts", "voice.shortcuts.d", true, nil),
				kit.Pad(th, 16, 20, p.hotkeys.Layout(th, l, nil)),
			))
		}
		if p.dictate != nil {
			shortcuts = append(shortcuts, panelOf(th,
				setting(th, l, "dictation.title", "dictation.title.d", true, nil),
				kit.Pad(th, 16, 20, p.dictate.Layout(th, l, nil)),
				setting(th, l, "dictation.clipboard", "dictation.clipboard.d", true, toggle(&p.dictClip, "dictation.clipboard")),
			))
		}
		return append(shortcuts, panelOf(th,
			setting(th, l, "tts.provider", "tts.provider.d", true, func(gtx layout.Context) layout.Dimensions {
				name := kit.Text(th, 14, voiceName(l, cur.TTS), th.Pal.Subtle).Layout
				if p.changeVoice == nil {
					return name(gtx)
				}
				return kit.HStack(th, 12, name, func(gtx layout.Context) layout.Dimensions {
					return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: T(l, "tts.change")}.Layout(gtx, th, &p.voiceBtn)
				})(gtx)
			}),
			setting(th, l, "voice.hotkeyMode", "voice.hotkeyMode.d", false, func(gtx layout.Context) layout.Dimensions {
				return kit.Segmented(gtx, th, &p.hotkey, []kit.Option{
					{Key: string(settings.HotkeyHold), Label: T(l, "voice.hold")},
					{Key: string(settings.HotkeyToggle), Label: T(l, "voice.toggle")},
					{Key: string(settings.HotkeyHandsfree), Label: T(l, "voice.handsfree")},
				})
			}),
			setting(th, l, "voice.language", "voice.language.d", false, func(gtx layout.Context) layout.Dimensions {
				return kit.Text(th, 14, lang, th.Pal.Subtle).Layout(gtx)
			}),
			setting(th, l, "tts.speed", "", false, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(260)))
				return kit.HStack(th, 12, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(th.Dp(190))
					gtx.Constraints.Max.X = gtx.Constraints.Min.X
					return p.speed.Layout(gtx, th, T(l, "tts.speed"))
				}, kit.Code(th, Tf(l, "tts.speedValue", speed)).Layout)(gtx)
			}),
		))
	case secScreen:
		rows := []layout.Widget{panelOf(th,
			setting(th, l, "screen.target", "screen.target.d", false, func(gtx layout.Context) layout.Dimensions {
				return kit.Segmented(gtx, th, &p.screenTarget, []kit.Option{
					{Key: string(settings.ScreenActive), Label: T(l, "screen.active")},
					{Key: string(settings.ScreenAll), Label: T(l, "screen.all")},
				})
			}),
			setting(th, l, "screen.keepShots", "screen.keepShots.d", true, toggle(&p.keepShots, "screen.keepShots")),
		)}
		if cur.Screen.KeepScreenshots && !cur.Privacy.SaveTranscripts {
			// Kept pictures hang off stored messages, so without saved
			// conversations the switch does nothing.
			rows = append(rows, kit.Small(th, T(l, "screen.keepShots.needsSave")).Layout)
		}
		return rows
	case secPrivacy:
		return []layout.Widget{panelOf(th,
			setting(th, l, "privacy.saveText", "privacy.saveText.d", true, toggle(&p.saveText, "privacy.saveText")),
			setting(th, l, "privacy.shareDiag", "privacy.shareDiag.d", false, toggle(&p.shareDiag, "privacy.shareDiag")),
		), kit.Small(th, T(l, "privacy.note")).Layout}
	case secMemory:
		rows := []layout.Widget{panelOf(th,
			setting(th, l, "memory.enabled", "memory.enabled.d", true, toggle(&p.memory, "memory.enabled")),
		)}
		if !cur.Memory.Enabled {
			rows = append(rows, kit.Small(th, T(l, "memory.off")).Layout)
		}
		if p.mem != nil {
			rows = append(rows, p.mem.rows(gtx, th, l)...)
		}
		return rows
	}
	names := make([]string, len(i18n.Languages))
	sel := 0
	for i, lg := range i18n.Languages {
		names[i] = lg.Native
		if string(lg.Code) == cur.UI.Language {
			sel = i
		}
	}
	return []layout.Widget{panelOf(th,
		setting(th, l, "platform.uiLang", "platform.uiLang.d", true, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(220)))
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return p.uiLang.Layout(gtx, th, names, sel, names[sel])
		}),
		setting(th, l, "platform.tray", "platform.tray.d", true, toggle(&p.tray, "platform.tray")),
		setting(th, l, "platform.login", "platform.login.d", false, toggle(&p.login, "platform.login")),
	), p.updateRows(th, l, toggle)}
}

// updateRows is the General section's update panel: the two preferences
// and, with an updater, the running version, last check and "Check now".
func (p *settingsPage) updateRows(th *theme.Theme, l Lang, toggle func(*widget.Bool, string) layout.Widget) layout.Widget {
	rows := []layout.Widget{
		setting(th, l, "updates.auto", "updates.auto.d", true, toggle(&p.autoUpd, "updates.auto")),
		setting(th, l, "updates.pre", "updates.pre.d", true, toggle(&p.preUpd, "updates.pre")),
	}
	if p.updates != nil {
		st := p.updates.State()
		r := kit.SettingRow{
			Title: Tf(l, "updates.version", st.Current.String()),
			Desc:  updateStatus(l, st, time.Now()),
			Control: func(gtx layout.Context) layout.Dimensions {
				if st.Checking || st.Busy() {
					gtx = gtx.Disabled()
				}
				return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: T(l, "updates.checkNow")}.Layout(gtx, th, &p.checkNow)
			},
		}
		rows = append(rows, func(gtx layout.Context) layout.Dimensions { return r.Layout(gtx, th) })
	}
	return panelOf(th, rows...)
}
