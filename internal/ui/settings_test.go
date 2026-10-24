package ui

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/ui/theme"
)

func TestSpeedMapping(t *testing.T) {
	tests := []struct {
		slider float32
		want   float64
	}{
		{0, 0.5}, {0.5, 1.0}, {1, 1.5}, {-1, 0.5}, {2, 1.5}, {0.26, 0.75}, {0.31, 0.8},
	}
	for _, tt := range tests {
		if got := speedFromSlider(tt.slider); got != tt.want {
			t.Errorf("speedFromSlider(%v) = %v, want %v", tt.slider, got, tt.want)
		}
	}
	for _, s := range []float64{0.5, 0.75, 1, 1.5} {
		if got := speedFromSlider(sliderFromSpeed(s)); got != s {
			t.Errorf("round trip %v -> %v", s, got)
		}
	}
}

func TestStepSpeed(t *testing.T) {
	tests := []struct {
		cur  float64
		dir  int
		want float64
	}{
		{1, 1, 1.1}, {1, -1, 0.9}, {1.45, 1, 1.5}, {1.5, 1, 1.5}, {0.5, -1, 0.5}, {0.55, -1, 0.5},
	}
	for _, tt := range tests {
		if got := stepSpeed(tt.cur, tt.dir); got != tt.want {
			t.Errorf("stepSpeed(%v, %d) = %v, want %v", tt.cur, tt.dir, got, tt.want)
		}
	}
	// Ten steps up from the minimum must land exactly on the maximum and
	// stay valid for settings.Validate the whole way.
	v := speedMin
	for i := 0; i < 10; i++ {
		v = stepSpeed(v, 1)
		d := settings.Defaults()
		d.Voice.TTSSpeed = v
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if v != speedMax {
		t.Errorf("after 10 steps = %v", v)
	}
}

type failingStore struct{ SettingsStore }

func (failingStore) Update(context.Context, func(*settings.Settings)) error {
	return errors.New("disk full")
}

func TestSettingsPageApplyAndSync(t *testing.T) {
	svc := newSettings(t)
	p := newSettingsPage(svc)
	p.apply(func(s *settings.Settings) { s.Screen.Target = settings.ScreenAll; s.Memory.Enabled = false })
	if p.saveErr {
		t.Fatal("unexpected save error")
	}
	p.sync(svc.Get())
	if p.screenTarget.Value != "all" || p.memory.Value {
		t.Errorf("sync: target=%q memory=%v", p.screenTarget.Value, p.memory.Value)
	}

	// An invalid value is rejected by the service and the view snaps back.
	p.apply(func(s *settings.Settings) { s.Voice.TTSSpeed = 9 })
	if !p.saveErr || svc.Get().Voice.TTSSpeed != 1 {
		t.Errorf("invalid update: saveErr=%v speed=%v", p.saveErr, svc.Get().Voice.TTSSpeed)
	}

	fp := newSettingsPage(failingStore{svc})
	fp.memory.Value = true
	fp.apply(func(s *settings.Settings) { s.Memory.Enabled = true })
	fp.sync(svc.Get())
	if !fp.saveErr || fp.memory.Value {
		t.Errorf("failed write: saveErr=%v memory=%v", fp.saveErr, fp.memory.Value)
	}
}

// Settings → Voice carries the same shortcut editor as setup, in the
// system's words only.
func TestSettingsVoiceShortcutEditor(t *testing.T) {
	h := newHarness(t, ModeMain, nil)
	m, _ := h.mainBuilt()
	m.showSettings(secVoice)
	th := theme.New(false, 1)
	r := new(input.Router)
	frame(r, h.sh, th, m)
	frame(r, h.sh, th, m)
	sp, ok := m.pages[RouteSettings].(*settingsPage)
	if !ok || sp.hotkeys == nil {
		t.Fatal("no shortcut editor in Settings")
	}
	for _, l := range []Lang{LangEN, LangRU} {
		var texts []string
		gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Source: r.Source(),
			Constraints: layout.Exact(image.Pt(700, 400))}
		sp.hotkeys.Layout(th, l, &texts)(gtx)
		joined := strings.Join(texts, "|")
		for _, want := range []string{"Super", "Space", "Backspace", T(l, "hotkey.apply")} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: editor lacks %q: %s", l, want, joined)
			}
		}
		for _, s := range texts {
			for _, n := range environmentNames {
				if strings.Contains(strings.ToLower(s), n) {
					t.Errorf("%q names %q", s, n)
				}
			}
		}
	}
}
