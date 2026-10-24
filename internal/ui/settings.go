package ui

import (
	"context"
	"math"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/ui/theme"
)

// TTS speed range mirrors settings.Validate; the slider snaps to sliderSnap
// and the Slower/Faster buttons (the keyboard path) move by buttonStep.
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

// settingsPage renders FR-UI-004 groups bound to the settings service. The
// widgets are a view: every frame they are reset from the service, so a
// rejected update snaps back to the stored value.
type settingsPage struct {
	store SettingsStore
	list  widget.List

	hotkey, screenTarget, uiLang widget.Enum

	speed          widget.Float
	speedPending   bool
	slower, faster widget.Clickable

	keepShots, memory, saveText, shareDiag, login, tray widget.Bool

	saveErr bool
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
	if p.hotkey.Update(gtx) {
		v := settings.HotkeyMode(p.hotkey.Value)
		p.apply(func(s *settings.Settings) { s.Voice.HotkeyMode = v })
	}
	if p.screenTarget.Update(gtx) {
		v := settings.ScreenTarget(p.screenTarget.Value)
		p.apply(func(s *settings.Settings) { s.Screen.Target = v })
	}
	if p.uiLang.Update(gtx) {
		v := p.uiLang.Value
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
	} {
		if b.w.Update(gtx) {
			v, set := b.w.Value, b.set
			p.apply(func(s *settings.Settings) { set(s, v) })
		}
	}
	if p.speed.Update(gtx) {
		p.speedPending = true
	}
	// Persist a drag once it ends, not on every motion event.
	if p.speedPending && !p.speed.Dragging() {
		p.speedPending = false
		v := speedFromSlider(p.speed.Value)
		p.apply(func(s *settings.Settings) { s.Voice.TTSSpeed = v })
	}
	for _, b := range []struct {
		w   *widget.Clickable
		dir int
	}{{&p.slower, -1}, {&p.faster, 1}} {
		if b.w.Clicked(gtx) {
			dir := b.dir
			p.apply(func(s *settings.Settings) { s.Voice.TTSSpeed = stepSpeed(s.Voice.TTSSpeed, dir) })
		}
	}
}

// sync resets the widgets from the stored settings.
func (p *settingsPage) sync(s settings.Settings) {
	p.hotkey.Value = string(s.Voice.HotkeyMode)
	p.screenTarget.Value = string(s.Screen.Target)
	p.uiLang.Value = s.UI.Language
	p.keepShots.Value = s.Screen.KeepScreenshots
	p.memory.Value = s.Memory.Enabled
	p.saveText.Value = s.Privacy.SaveTranscripts
	p.shareDiag.Value = s.Privacy.ShareDiagnostics
	p.login.Value = s.Platform.LaunchAtLogin
	p.tray.Value = s.Platform.TrayIcon
	if !p.speed.Dragging() && !p.speedPending {
		p.speed.Value = sliderFromSpeed(s.Voice.TTSSpeed)
	}
}

func (p *settingsPage) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	p.update(gtx)
	cur := p.store.Get()
	p.sync(cur)

	radio := func(e *widget.Enum, key, label string) layout.FlexChild {
		return layout.Rigid(material.RadioButton(th.Theme, e, key, label).Layout)
	}
	check := func(b *widget.Bool, label string) layout.FlexChild {
		return layout.Rigid(material.CheckBox(th.Theme, b, label).Layout)
	}
	label := func(txt string) layout.FlexChild { return layout.Rigid(body(th, txt).Layout) }
	later := layout.Rigid(muted(th, T(l, "settings.later")).Layout)

	speedNow := cur.Voice.TTSSpeed
	if p.speedPending || p.speed.Dragging() {
		speedNow = speedFromSlider(p.speed.Value)
	}
	lang := T(l, "voice.languageAuto")
	if cur.Voice.Language != "auto" {
		lang = cur.Voice.Language
	}

	groups := []struct {
		key  string
		rows []layout.FlexChild
	}{
		{"group.voice", []layout.FlexChild{
			label(T(l, "voice.hotkeyMode")),
			radio(&p.hotkey, string(settings.HotkeyHold), T(l, "voice.hold")),
			radio(&p.hotkey, string(settings.HotkeyToggle), T(l, "voice.toggle")),
			radio(&p.hotkey, string(settings.HotkeyHandsfree), T(l, "voice.handsfree")),
			vspace(8),
			label(Tf(l, "voice.language", lang)),
		}},
		{"group.models", p.modelRows(th, l, cur.Models)},
		{"group.tts", []layout.FlexChild{
			label(Tf(l, "tts.speed", speedNow)),
			vspace(4),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return button(gtx, th, &p.slower, T(l, "tts.slower"))
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: 12, Right: 12}.Layout(gtx, material.Slider(th.Theme, &p.speed).Layout)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return button(gtx, th, &p.faster, T(l, "tts.faster"))
					}),
				)
			}),
		}},
		{"group.screen", []layout.FlexChild{
			label(T(l, "screen.target")),
			radio(&p.screenTarget, string(settings.ScreenActive), T(l, "screen.active")),
			radio(&p.screenTarget, string(settings.ScreenAll), T(l, "screen.all")),
			check(&p.keepShots, T(l, "screen.keepShots")),
		}},
		{"group.memory", []layout.FlexChild{check(&p.memory, T(l, "memory.enabled"))}},
		{"group.agents", []layout.FlexChild{later}},
		{"group.integrations", []layout.FlexChild{later}},
		{"group.privacy", []layout.FlexChild{
			check(&p.saveText, T(l, "privacy.saveText")),
			check(&p.shareDiag, T(l, "privacy.shareDiag")),
		}},
		{"group.platform", []layout.FlexChild{
			label(T(l, "platform.uiLang")),
			radio(&p.uiLang, string(LangRU), "Русский"),
			radio(&p.uiLang, string(LangEN), "English"),
			check(&p.login, T(l, "platform.login")),
			check(&p.tray, T(l, "platform.tray")),
		}},
	}

	var head []layout.Widget
	head = append(head, heading(th, T(l, "settings.heading")).Layout)
	if p.saveErr {
		head = append(head, th.StatusLabel(theme.StatusError, T(l, "settings.saveError")).Layout)
	}
	n := len(head) + len(groups)
	return layout.UniformInset(24).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.List(th.Theme, &p.list).Layout(gtx, n, func(gtx layout.Context, i int) layout.Dimensions {
			if i < len(head) {
				return layout.Inset{Bottom: 12}.Layout(gtx, head[i])
			}
			g := groups[i-len(head)]
			return layout.Inset{Bottom: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return card(gtx, th, func(gtx layout.Context) layout.Dimensions {
					rows := append([]layout.FlexChild{layout.Rigid(subheading(th, T(l, g.key)).Layout), vspace(8)}, g.rows...)
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
				})
			})
		})
	})
}

func (p *settingsPage) modelRows(th *theme.Theme, l Lang, m settings.Models) []layout.FlexChild {
	roles := []struct{ key, val string }{
		{"models.talk", m.Talk}, {"models.vision", m.Vision}, {"models.agent", m.Agent},
		{"models.cleanup", m.DictationCleanup}, {"models.fast", m.Fast}, {"models.deep", m.Deep},
	}
	rows := make([]layout.FlexChild, 0, len(roles)+1)
	for _, r := range roles {
		v := r.val
		if v == "" {
			v = T(l, "settings.notSet")
		}
		rows = append(rows, layout.Rigid(body(th, T(l, r.key)+": "+v).Layout))
	}
	return append(rows, vspace(4), layout.Rigid(muted(th, T(l, "settings.later")).Layout))
}
