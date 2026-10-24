package ui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/xlsft/companion/internal/chat"
	"github.com/xlsft/companion/internal/ui/theme"
)

// fakeChat is a ChatBackend the test feeds views into.
type fakeChat struct {
	caps    chat.Capabilities
	views   chan chat.View
	sendErr error

	mu    sync.Mutex
	calls []string
	sent  []string
}

func newFakeChat(caps chat.Capabilities) *fakeChat {
	return &fakeChat{caps: caps, views: make(chan chat.View, 16)}
}

func (f *fakeChat) record(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeChat) Capabilities() chat.Capabilities       { return f.caps }
func (f *fakeChat) Subscribe() (<-chan chat.View, func()) { return f.views, func() {} }
func (f *fakeChat) ToggleVoice(context.Context) error     { f.record("voice"); return nil }
func (f *fakeChat) Stop(context.Context) error            { f.record("stop"); return nil }
func (f *fakeChat) NewChat(context.Context) error         { f.record("new"); return nil }
func (f *fakeChat) Send(_ context.Context, text string) error {
	f.mu.Lock()
	f.sent = append(f.sent, text)
	f.mu.Unlock()
	f.record("send")
	return f.sendErr
}

func (f *fakeChat) called() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

// waitFor renders frames until cond holds.
func waitFor(t *testing.T, r *input.Router, h *harness, th *theme.Theme, m *mainUI, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		frame(r, h.sh, th, m)
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func homeWith(t *testing.T, be ChatBackend, log *slog.Logger) (*harness, *mainUI, *homePage) {
	t.Helper()
	h := newHarness(t, ModeMain, nil)
	m, _ := h.mainBuilt()
	m.chat = newChatModel(be, log, func() {})
	if log == nil {
		m.chat.log = slog.New(slog.DiscardHandler)
	}
	m.navigate(RouteHome)
	return h, m, m.page(RouteHome).(*homePage)
}

var at = time.Date(2026, 10, 24, 20, 31, 0, 0, time.Local)

// The streamed reply reaches the rendered message as it grows.
func TestHomeStreaming(t *testing.T) {
	user := chat.Message{Role: chat.RoleUser, Text: "How do I make it transparent?", Via: chat.ViaVoice, STTModel: "whisper medium", At: at}
	tests := []struct {
		name  string
		view  chat.View
		texts []string
	}{
		{"thinking", chat.View{Messages: []chat.Message{user}, Phase: chat.PhaseThinking}, []string{user.Text}},
		{"first chunk", chat.View{Messages: []chat.Message{user, {Role: chat.RoleAssistant, Text: "Select", Streaming: true}}, Phase: chat.PhaseResponding}, []string{user.Text, "Select"}},
		{"more chunks", chat.View{Messages: []chat.Message{user, {Role: chat.RoleAssistant, Text: "Select the frame", Streaming: true}}, Phase: chat.PhaseSpeaking}, []string{user.Text, "Select the frame"}},
		{"done", chat.View{Messages: []chat.Message{user, {Role: chat.RoleAssistant, Text: "Select the frame."}}, Phase: chat.PhaseIdle}, []string{user.Text, "Select the frame."}},
	}
	be := newFakeChat(chat.Capabilities{Text: true, Voice: true})
	h, m, p := homeWith(t, be, nil)
	th := theme.New(true, 1)
	r := new(input.Router)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			be.views <- tt.view
			waitFor(t, r, h, th, m, "texts", func() bool {
				if len(p.sels) < len(tt.texts) {
					return false
				}
				for i, want := range tt.texts {
					if p.sels[i].Text() != want {
						return false
					}
				}
				return true
			})
		})
	}
}

func TestHomeControls(t *testing.T) {
	tests := []struct {
		name  string
		caps  chat.Capabilities
		phase chat.Phase
		click func(p *homePage)
		want  string
	}{
		{"stop while speaking", chat.Capabilities{Text: true}, chat.PhaseSpeaking, func(p *homePage) { p.stopBtn.Click() }, "stop"},
		{"mic with voice", chat.Capabilities{Text: true, Voice: true}, chat.PhaseIdle, func(p *homePage) { p.micBtn.Click() }, "voice"},
		{"mic without voice", chat.Capabilities{Text: true}, chat.PhaseIdle, func(p *homePage) { p.micBtn.Click() }, ""},
		{"send typed text", chat.Capabilities{Text: true}, chat.PhaseIdle, func(p *homePage) { p.editor.SetText("hi"); p.sendBtn.Click() }, "send"},
		{"send blank", chat.Capabilities{Text: true}, chat.PhaseIdle, func(p *homePage) { p.editor.SetText("  "); p.sendBtn.Click() }, ""},
		{"send without text capability", chat.Capabilities{}, chat.PhaseIdle, func(p *homePage) { p.editor.SetText("hi"); p.sendBtn.Click() }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			be := newFakeChat(tt.caps)
			h, m, p := homeWith(t, be, nil)
			th := theme.New(true, 1)
			r := new(input.Router)
			be.views <- chat.View{Messages: []chat.Message{{Role: chat.RoleUser, Text: "q"}}, Phase: tt.phase}
			waitFor(t, r, h, th, m, "view", func() bool { return len(p.sels) == 1 })
			tt.click(p)
			frame(r, h.sh, th, m)
			if tt.want == "" {
				time.Sleep(20 * time.Millisecond)
				if got := be.called(); got != "" {
					t.Fatalf("called %q", got)
				}
				return
			}
			waitFor(t, r, h, th, m, tt.want, func() bool { return be.called() == tt.want })
		})
	}
}

// Enter in the composer sends and clears it; Ctrl+N starts a new chat.
func TestHomeKeyboard(t *testing.T) {
	be := newFakeChat(chat.Capabilities{Text: true})
	h, m, p := homeWith(t, be, nil)
	h.sh.mu.Lock()
	h.sh.main = m
	h.sh.mu.Unlock()
	th := theme.New(false, 1)
	r := new(input.Router)
	frame(r, h.sh, th, m)
	p.editor.SetText("what is this?")
	frame(r, h.sh, th, m)
	r.Source().Execute(key.FocusCmd{Tag: &p.editor})
	frame(r, h.sh, th, m)
	if !r.Source().Focused(&p.editor) {
		t.Fatal("composer not focused")
	}
	r.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	waitFor(t, r, h, th, m, "send", func() bool { return be.called() == "send" })
	if p.editor.Text() != "" || be.sent[0] != "what is this?" {
		t.Errorf("editor %q, sent %q", p.editor.Text(), be.sent)
	}
	r.Queue(key.Event{Name: "N", Modifiers: key.ModShortcut, State: key.Press})
	waitFor(t, r, h, th, m, "new chat", func() bool { return be.called() == "send,new" })
}

// Without a backend Home says so and nothing can be sent.
func TestHomeWithoutBackend(t *testing.T) {
	h, m, p := homeWith(t, nil, nil)
	th := theme.New(false, 1)
	r := new(input.Router)
	frame(r, h.sh, th, m)
	if !p.editor.ReadOnly {
		t.Error("composer editable without a backend")
	}
	p.editor.SetText("hi")
	p.sendBtn.Click()
	p.stopBtn.Click()
	frame(r, h.sh, th, m)
	if p.editor.Text() != "hi" {
		t.Error("text was taken although nothing can send it")
	}
	if _, caps, _ := m.chat.snapshot(); caps != (chat.Capabilities{}) {
		t.Errorf("caps %+v", caps)
	}
	m.newChat() // must not panic
}

// A failed send is shown and logged by code only.
func TestHomeFailureNotLogged(t *testing.T) {
	var buf syncBuffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	be := newFakeChat(chat.Capabilities{Text: true})
	be.sendErr = errors.New("session manager closed")
	h, m, p := homeWith(t, be, log)
	th := theme.New(false, 1)
	r := new(input.Router)
	frame(r, h.sh, th, m)
	p.editor.SetText("my secret prompt")
	p.sendBtn.Click()
	waitFor(t, r, h, th, m, "failure", func() bool { _, _, k := m.chat.snapshot(); return k == "chat.error.send" })
	if out := buf.String(); strings.Contains(out, "secret") || !strings.Contains(out, "op=send") {
		t.Errorf("log: %s", out)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// demoView is the prototype's conversation, for snapshots.
func demoView() chat.View {
	return chat.View{
		Title: "Прозрачный фон в Figma",
		Phase: chat.PhaseSpeaking,
		Messages: []chat.Message{
			{Role: chat.RoleUser, Text: "Как сделать фон у этого фрейма прозрачным?", Via: chat.ViaVoice, STTModel: "whisper medium", At: at},
			{Role: chat.RoleAssistant, Text: "Выделите фрейм и в правой панели в блоке Fill уберите заливку кнопкой «−». Я обвёл этот блок на экране."},
			{Role: chat.RoleUser, Text: "А чтобы при экспорте в PNG тоже было прозрачно?", At: at.Add(time.Minute)},
			{Role: chat.RoleAssistant, Text: "При экспорте оставьте формат PNG — пустая заливка сохранится как прозрачность. Внизу правой панели в блоке Export", Streaming: true},
		},
	}
}

// TestHomeSnapshots renders the main window at the prototypes' size when
// an offscreen GL context exists. Set OC_UI_SNAPSHOT_DIR to keep PNGs.
func TestHomeSnapshots(t *testing.T) {
	size := image.Pt(1280, 800)
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skipf("no headless GPU context on this machine: %v", err)
	}
	defer w.Release()
	shots := []struct {
		name string
		be   func() ChatBackend
		dark bool
		lang string
	}{
		{"home-chat-dark", func() ChatBackend {
			f := newFakeChat(chat.Capabilities{Text: true, Voice: true, Hotkey: "Mod+Space", Model: "claude-sonnet-5-5", Speech: "ElevenLabs · Алиса"})
			f.views <- demoView()
			return f
		}, true, "ru"},
		{"home-chat-light", func() ChatBackend {
			f := newFakeChat(chat.Capabilities{Text: true, Voice: true, Hotkey: "Mod+Space", Model: "claude-sonnet-5-5"})
			f.views <- demoView()
			return f
		}, false, "ru"},
		{"home-long-dark", func() ChatBackend {
			f := newFakeChat(chat.Capabilities{Text: true})
			v := demoView()
			done := append([]chat.Message(nil), v.Messages[:3]...)
			v.Messages = append(append(append([]chat.Message(nil), done...), done...), v.Messages...)
			v.Phase = chat.PhaseResponding
			f.views <- v
			return f
		}, true, "en"},
		{"home-empty-dark", func() ChatBackend {
			f := newFakeChat(chat.Capabilities{Text: true, Voice: true, Hotkey: "Mod+Space"})
			f.views <- chat.View{}
			return f
		}, true, "en"},
		{"home-offline-dark", func() ChatBackend { return nil }, true, "ru"},
	}
	dir := os.Getenv("OC_UI_SNAPSHOT_DIR")
	for _, s := range shots {
		be := s.be()
		h, m, _ := homeWith(t, be, nil)
		h.sh.mu.Lock()
		h.sh.lang, h.sh.dark = langOf(s.lang), s.dark
		h.sh.mu.Unlock()
		th := theme.New(s.dark, 1)
		r := new(input.Router)
		render := func() *op.Ops {
			ops := new(op.Ops)
			gtx := layout.Context{Ops: ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: time.Now(), Source: r.Source(), Constraints: layout.Exact(size)}
			renderFrame(gtx, th, h.sh, m)
			r.Frame(ops)
			return ops
		}
		render()
		if f, ok := be.(*fakeChat); ok {
			deadline := time.Now().Add(2 * time.Second)
			for len(f.views) > 0 && time.Now().Before(deadline) {
				render()
				time.Sleep(time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
		}
		render()
		ops := render()
		if err := w.Frame(ops); err != nil {
			t.Fatalf("%s: frame: %v", s.name, err)
		}
		img := image.NewRGBA(image.Rectangle{Max: size})
		if err := w.Screenshot(img); err != nil {
			t.Fatalf("%s: screenshot: %v", s.name, err)
		}
		if uniform(img) {
			t.Errorf("%s: frame is a single colour", s.name)
		}
		if dir != "" {
			f, err := os.Create(filepath.Join(dir, s.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, img); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}
