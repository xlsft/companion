package ui

import (
	"image"
	"strings"
	"testing"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/xlsft/companion/internal/dictation"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/ui/theme"
)

func TestDictationBannerFor(t *testing.T) {
	tests := []struct {
		name   string
		st     dictation.State
		chord  string
		shown  bool
		status theme.Status
		title  string
		detail string
	}{
		{name: "idle", st: dictation.State{}},
		{name: "listening", st: dictation.State{Phase: dictation.Listening}, chord: "Ctrl+Alt+Space", shown: true,
			title: "dictation.listening", detail: "Ctrl+Alt+Space"},
		{name: "listening without a chord", st: dictation.State{Phase: dictation.Listening}, shown: true,
			title: "dictation.listening", detail: "dictation.listening.noKey"},
		{name: "transcribing", st: dictation.State{Phase: dictation.Transcribing}, shown: true, title: "dictation.transcribing"},
		{name: "typing", st: dictation.State{Phase: dictation.Typing}, shown: true, title: "dictation.typing"},
		{name: "nothing heard", st: dictation.State{Err: dictation.CodeNoSpeech}, shown: true, status: theme.StatusWarn,
			title: "dictation.failed", detail: "dictation.err.no_speech"},
		{name: "cannot type", st: dictation.State{Err: dictation.CodeUnsupported}, shown: true, status: theme.StatusError,
			title: "dictation.failed", detail: "dictation.err.unsupported"},
		{name: "unknown code", st: dictation.State{Err: "weird"}, shown: true, status: theme.StatusError,
			title: "dictation.failed", detail: "dictation.err.insert_failed"},
	}
	for _, tt := range tests {
		for _, l := range []Lang{LangEN, LangRU} {
			b, ok := dictationBannerFor(l, tt.st, tt.chord)
			if ok != tt.shown {
				t.Errorf("%s: shown %v", tt.name, ok)
				continue
			}
			if !ok {
				continue
			}
			if b.Status != tt.status || b.Title != T(l, tt.title) {
				t.Errorf("%s %s: %+v", tt.name, l, b)
			}
			want := tt.detail
			if strings.HasPrefix(want, "dictation.") {
				want = T(l, want)
			}
			if !strings.Contains(b.Detail, want) {
				t.Errorf("%s %s: detail %q lacks %q", tt.name, l, b.Detail, want)
			}
		}
	}
}

// Settings → Voice has the dictation editor and the clipboard switch, in
// the system's words only.
func TestSettingsVoiceDictation(t *testing.T) {
	h := newHarness(t, ModeMain, nil)
	m, _ := h.mainBuilt()
	m.showSettings(secVoice)
	th := theme.New(false, 1)
	r := new(input.Router)
	frame(r, h.sh, th, m)
	frame(r, h.sh, th, m)
	sp, ok := m.pages[RouteSettings].(*settingsPage)
	if !ok || sp.dictate == nil {
		t.Fatal("no dictation editor in Settings")
	}
	for _, l := range []Lang{LangEN, LangRU} {
		var texts []string
		gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Source: r.Source(),
			Constraints: layout.Exact(image.Pt(700, 400))}
		sp.dictate.Layout(th, l, &texts)(gtx)
		joined := strings.Join(texts, "|")
		for _, want := range []string{"Ctrl", "Alt", "Space", T(l, "dictation.shortcut"), T(l, "hotkey.apply")} {
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
	svc := h.sh.opts.Settings
	sp.apply(func(s *settings.Settings) { s.Voice.DictationClipboard = true })
	sp.sync(svc.Get())
	if !sp.dictClip.Value || !svc.Get().Voice.DictationClipboard {
		t.Error("clipboard switch not stored")
	}
}
