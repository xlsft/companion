package setupcheck

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/stt/supervisor"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/pkg/contracts"
)

const (
	pMic     = string(contracts.CapMicrophone)
	pScreen  = string(contracts.CapScreenCapture)
	pHotkey  = string(contracts.CapGlobalHotkey)
	pOverlay = string(contracts.CapOverlayClickThrough)
)

func TestPermissions(t *testing.T) {
	granted := readiness.PermGranted
	tests := []struct {
		name   string
		edit   func(*Permissions)
		status readiness.CheckStatus
		code   string
		detail map[string]string
	}{
		{name: "all granted", status: readiness.Passed,
			detail: map[string]string{pMic: granted, pScreen: granted, pHotkey: granted, pOverlay: granted}},
		{name: "mic missing", edit: func(p *Permissions) { p.Mic = &fakeMic{openErr: audio.ErrDeviceNotFound} },
			status: readiness.Failed, code: CodeMicNotFound,
			detail: map[string]string{pMic: CodeMicNotFound, pScreen: granted, pHotkey: granted, pOverlay: granted}},
		{name: "mic denied", edit: func(p *Permissions) { p.Mic = &fakeMic{openErr: contracts.PermissionDenied(contracts.CapMicrophone)} },
			status: readiness.Failed, code: readiness.PermDenied},
		{name: "mic silent end", edit: func(p *Permissions) { p.Mic = &fakeMic{frames: 2} },
			status: readiness.Failed, code: CodeMicOpenFailed},
		{name: "mic lost", edit: func(p *Permissions) { p.Mic = &fakeMic{frames: 1, end: audio.ErrDeviceLost} },
			status: readiness.Failed, code: CodeMicLost},
		{name: "no capture backend", edit: func(p *Permissions) {
			p.Screen, p.ScreenErr = nil, contracts.Unsupported(contracts.CapScreenCapture)
		}, status: readiness.Failed, code: readiness.PermUnavailable},
		{name: "capture denied", edit: func(p *Permissions) {
			p.Screen = &fakeCapturer{err: contracts.PermissionDenied(contracts.CapScreenCapture)}
		}, status: readiness.Failed, code: readiness.PermDenied},
		{name: "capture empty", edit: func(p *Permissions) { p.Screen = &fakeCapturer{empty: true} },
			status: readiness.Failed, code: CodeCaptureFailed},
		{name: "hotkey needs binding", edit: func(p *Permissions) {
			p.Hotkey = func() string { return readiness.PermNeedsBinding }
		}, status: readiness.Failed, code: readiness.PermNeedsBinding,
			detail: map[string]string{pMic: granted, pScreen: granted, pHotkey: readiness.PermNeedsBinding, pOverlay: granted}},
		{name: "overlay host missing, panel", edit: func(p *Permissions) {
			p.Overlay, p.OverlayErr = nil, contracts.Unsupported(contracts.CapOverlayClickThrough)
		}, status: readiness.Passed,
			detail: map[string]string{pMic: granted, pScreen: granted, pHotkey: granted, pOverlay: readiness.PermFallback}},
		{name: "overlay host missing, no panel", edit: func(p *Permissions) {
			p.Overlay, p.PanelFallback = nil, false
			p.OverlayErr = contracts.Unsupported(contracts.CapOverlayClickThrough)
		}, status: readiness.Failed, code: readiness.PermUnavailable},
		{name: "overlay present fails, panel", edit: func(p *Permissions) { p.Overlay = &fakeHost{presentErr: errBoom} },
			status: readiness.Passed},
		{name: "overlay open fails, no panel", edit: func(p *Permissions) {
			p.Overlay, p.PanelFallback = &fakeHost{openErr: errBoom}, false
		}, status: readiness.Failed, code: CodeOverlayFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := &fakeHost{}
			p := Permissions{
				Mic: &fakeMic{frames: 10}, Device: func() string { return "dev1" },
				Screen: &fakeCapturer{}, Hotkey: func() string { return readiness.PermGranted },
				Overlay: host, Topology: oneDisplay, PanelFallback: true,
			}
			if tt.edit != nil {
				tt.edit(&p)
			}
			got := p.Run(context.Background())
			if got.Status != tt.status || got.Code != tt.code {
				t.Fatalf("got %s/%q, want %s/%q (detail %v)", got.Status, got.Code, tt.status, tt.code, got.Detail)
			}
			if tt.detail != nil && !maps.Equal(got.Detail, tt.detail) {
				t.Errorf("detail = %v, want %v", got.Detail, tt.detail)
			}
			if len(got.Detail) != 4 {
				t.Errorf("detail has %d entries, want one per capability", len(got.Detail))
			}
			if tt.edit == nil && (host.presented != 1 || host.closed != 1) {
				t.Errorf("overlay presented %d closed %d, want 1/1", host.presented, host.closed)
			}
		})
	}
}

func TestPermissionsMicDevice(t *testing.T) {
	mic := &fakeMic{frames: 10}
	Permissions{Mic: mic, Device: func() string { return "usb" }}.microphone(context.Background())
	if mic.device != "usb" {
		t.Errorf("opened %q, want the selected device", mic.device)
	}
}

func TestHotkeyPermission(t *testing.T) {
	tests := []struct {
		name string
		s    HotkeyState
		want string
	}{
		{"no service", HotkeyState{}, readiness.PermUnavailable},
		{"primary", HotkeyState{Running: true, Primary: true}, readiness.PermGranted},
		{"socket only", HotkeyState{Running: true}, readiness.PermNeedsBinding},
		{"socket bound", HotkeyState{Running: true, CtlSeen: true}, readiness.PermGranted},
		{"portal denied", HotkeyState{Running: true, Primary: true, Err: contracts.PermissionDenied(contracts.CapGlobalHotkey)}, readiness.PermDenied},
		{"conflict", HotkeyState{Running: true, Primary: true, Err: fmt.Errorf("talk: %w", contracts.ErrHotkeyConflict)}, "hotkey_conflict"},
		{"primary other error, socket bound", HotkeyState{Running: true, Primary: true, Err: errBoom, CtlSeen: true}, readiness.PermGranted},
		{"primary other error", HotkeyState{Running: true, Primary: true, Err: errBoom}, readiness.PermNeedsBinding},
	}
	for _, tt := range tests {
		if got := HotkeyPermission(tt.s); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func speech(text string) stt.Final { return stt.Final{Text: text, Speech: text != ""} }

func TestSTT(t *testing.T) {
	ru := readiness.MicPhrase("ru")
	tests := []struct {
		name  string
		lang  string
		mic   *fakeMic
		stt   *fakeSTT
		edit  func(*Listener)
		code  string
		heard string
	}{
		{name: "match", lang: "ru", stt: &fakeSTT{final: speech(ru + ".")}, heard: ru + "."},
		{name: "match en", lang: "en", stt: &fakeSTT{final: speech("hello, this is a microphone test")}, heard: "hello, this is a microphone test"},
		{name: "mismatch", lang: "ru", stt: &fakeSTT{final: speech("продолжение следует")}, code: CodeMismatch, heard: "продолжение следует"},
		{name: "silence", stt: &fakeSTT{final: stt.Final{}}, code: CodeNoSpeech},
		{name: "blank text", stt: &fakeSTT{final: stt.Final{Text: " ", Speech: true}}, code: CodeNoSpeech},
		{name: "no model", stt: &fakeSTT{}, edit: func(l *Listener) { l.Model = func() error { return errBoom } }, code: CodeNoModel},
		{name: "sidecar down", stt: &fakeSTT{}, edit: func(l *Listener) {
			l.Ready = func(context.Context) error { return supervisor.ErrDegraded }
		}, code: CodeSTTUnavailable},
		{name: "session refused", stt: &fakeSTT{startErr: supervisor.ErrNotReady}, code: CodeSTTUnavailable},
		{name: "mic missing", mic: &fakeMic{openErr: audio.ErrDeviceNotFound}, stt: &fakeSTT{}, code: CodeMicNotFound},
		{name: "mic lost", mic: &fakeMic{frames: 3, end: audio.ErrDeviceLost}, stt: &fakeSTT{noEndpoint: true}, code: CodeMicLost},
		{name: "sidecar died", stt: &fakeSTT{finishErr: supervisor.ErrInterrupted}, code: CodeSTTUnavailable},
		{name: "decode error", stt: &fakeSTT{finishErr: errBoom}, code: CodeSTTFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mic := tt.mic
			if mic == nil {
				mic = &fakeMic{frames: 20, hold: true}
			}
			l := listener(mic, tt.stt)
			if tt.edit != nil {
				tt.edit(l)
			}
			lang := tt.lang
			c := &STT{Listener: l, Lang: func() string { return lang }}
			got := c.Run(context.Background())
			want := readiness.Passed
			if tt.code != "" {
				want = readiness.Failed
			}
			if got.Status != want || got.Code != tt.code {
				t.Fatalf("got %s/%q, want %s/%q", got.Status, got.Code, want, tt.code)
			}
			if c.LastTranscript() != tt.heard {
				t.Errorf("transcript %q, want %q", c.LastTranscript(), tt.heard)
			}
			if got.Detail != nil {
				t.Errorf("detail %v: the transcript must not reach the result", got.Detail)
			}
		})
	}
}

func TestListenWindow(t *testing.T) {
	// Without an endpoint event the backstop timer ends the recording.
	f := &fakeSTT{final: speech("hi")}
	l := &Listener{Mic: &fakeMic{hold: true}, STT: f.start, Window: time.Millisecond}
	start := time.Now()
	if _, fl := l.Listen(context.Background(), "en"); fl != nil {
		t.Fatalf("failure %+v", fl)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestListenCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &Listener{Mic: &fakeMic{hold: true}, STT: (&fakeSTT{}).start}
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, f := l.Listen(ctx, "")
	if f == nil || f.Code != CodeCanceled {
		t.Fatalf("got %+v, want canceled", f)
	}
}

func TestSpeak(t *testing.T) {
	tests := []struct {
		name string
		sp   *fakeSpeaker
		conf func() bool
		code string
	}{
		{name: "plays", sp: &fakeSpeaker{}, conf: yes},
		{name: "not configured", sp: &fakeSpeaker{}, conf: func() bool { return false }, code: CodeNotConfigured},
		{name: "bad key", sp: &fakeSpeaker{err: &tts.Error{Kind: tts.ErrAuth}}, conf: yes, code: "auth"},
		{name: "unreachable", sp: &fakeSpeaker{err: &tts.Error{Kind: tts.ErrUnavailable}}, conf: yes, code: "network"},
		{name: "rate limited", sp: &fakeSpeaker{err: &tts.Error{Kind: tts.ErrRateLimited}}, conf: yes, code: "rate_limit"},
		{name: "device error", sp: &fakeSpeaker{err: errBoom}, conf: yes, code: CodePlayback},
		{name: "nothing audible", sp: &fakeSpeaker{silent: true}, conf: yes, code: CodePlayback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Speak{Speaker: tt.sp, Configured: tt.conf, Lang: func() string { return "en" }}.Run(context.Background())
			if got.Code != tt.code || (got.Status == readiness.Passed) != (tt.code == "") {
				t.Fatalf("got %s/%q, want %q", got.Status, got.Code, tt.code)
			}
			if tt.code == "" && (len(tt.sp.said) != 1 || tt.sp.said[0] != readiness.TTSSentence("en")) {
				t.Errorf("said %q, want the fixed sentence", tt.sp.said)
			}
		})
	}
}

func TestBYOK(t *testing.T) {
	ok := providers.StepResult{ID: providers.StepConnection, Status: providers.StatusOK}
	tests := []struct {
		name string
		f    *fakeProviders
		code string
	}{
		{name: "passes", f: &fakeProviders{list: []*providers.Info{{ID: "p1"}, {ID: "p2"}},
			res: providers.TestResult{ProviderID: "p1", Steps: []providers.StepResult{ok}}}},
		{name: "nothing configured", f: &fakeProviders{}, code: CodeNoProvider},
		{name: "store", f: &fakeProviders{listErr: errBoom}, code: "store_error"},
		{name: "auth fails", f: &fakeProviders{list: []*providers.Info{{ID: "p1"}}, res: providers.TestResult{ProviderID: "p1",
			Steps: []providers.StepResult{ok, {ID: providers.StepAuthModel, Status: providers.StatusFailed, Code: "auth"},
				{ID: providers.StepCapabilities, Status: providers.StatusSkipped}}}}, code: "auth"},
		{name: "failed without code", f: &fakeProviders{list: []*providers.Info{{ID: "p1"}}, res: providers.TestResult{
			Steps: []providers.StepResult{{ID: providers.StepConnection, Status: providers.StatusFailed}}}}, code: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BYOK{Providers: tt.f}.Run(context.Background())
			if got.Code != tt.code || (got.Status == readiness.Passed) != (tt.code == "") {
				t.Fatalf("got %s/%q, want %q", got.Status, got.Code, tt.code)
			}
			if len(tt.f.list) > 0 {
				if tt.f.tested != tt.f.list[0].ID {
					t.Errorf("tested %q, want the first provider", tt.f.tested)
				}
				if !maps.Equal(got.Detail, tt.f.res.Detail()) {
					t.Errorf("detail %v, want TestResult.Detail()", got.Detail)
				}
			}
		})
	}
}

func TestPlatformResult(t *testing.T) {
	st := func(s contracts.Status) contracts.CapabilityState { return contracts.CapabilityState{Status: s} }
	tests := []struct {
		name string
		caps map[contracts.Capability]contracts.CapabilityState
		code string
	}{
		{name: "niri-like", caps: map[contracts.Capability]contracts.CapabilityState{
			contracts.CapMicrophone: st(contracts.StatusAvailable), contracts.CapScreenCapture: st(contracts.StatusAvailable),
			contracts.CapGlobalHotkey: st(contracts.StatusDegraded), contracts.CapHotkeyRelease: st(contracts.StatusUnavailable),
			contracts.CapTray: st(contracts.StatusUnknown)}},
		{name: "no microphone", caps: map[contracts.Capability]contracts.CapabilityState{
			contracts.CapMicrophone: st(contracts.StatusUnavailable), contracts.CapScreenCapture: st(contracts.StatusAvailable)},
			code: readiness.PermUnavailable},
		{name: "no capture", caps: map[contracts.Capability]contracts.CapabilityState{
			contracts.CapMicrophone: st(contracts.StatusAvailable), contracts.CapScreenCapture: st(contracts.StatusUnavailable)},
			code: readiness.PermUnavailable},
		{name: "capture needs permission", caps: map[contracts.Capability]contracts.CapabilityState{
			contracts.CapMicrophone: st(contracts.StatusAvailable), contracts.CapScreenCapture: st(contracts.StatusNeedsPermission)}},
		{name: "unprobed", caps: nil},
	}
	for _, tt := range tests {
		got := PlatformResult(contracts.CapabilityReport{Capabilities: tt.caps})
		if got.Code != tt.code || (got.Status == readiness.Passed) != (tt.code == "") {
			t.Errorf("%s: got %s/%q, want %q", tt.name, got.Status, got.Code, tt.code)
		}
	}
}

func TestPlatformDetectFails(t *testing.T) {
	p := Platform{Detect: func(context.Context) (contracts.CapabilityReport, error) {
		return contracts.CapabilityReport{}, errBoom
	}}
	if got := p.Run(context.Background()); got.Code != CodeDetectFailed {
		t.Errorf("got %q", got.Code)
	}
	partial := contracts.CapabilityReport{Capabilities: map[contracts.Capability]contracts.CapabilityState{
		contracts.CapMicrophone: {Status: contracts.StatusAvailable}}}
	p.Detect = func(context.Context) (contracts.CapabilityReport, error) { return partial, errBoom }
	if got := p.Run(context.Background()); got.Status != readiness.Passed {
		t.Errorf("partial report: got %s/%q", got.Status, got.Code)
	}
}

func TestEndToEnd(t *testing.T) {
	en := readiness.MicPhrase("en")
	all := func(v map[string]string) map[string]string {
		out := map[string]string{}
		for _, s := range readiness.EndToEndStages {
			out[s] = readiness.DetailOK
		}
		maps.Copy(out, v)
		return out
	}
	skip := readiness.DetailSkipped
	tests := []struct {
		name   string
		edit   func(*EndToEnd, *fakeLLM, *fakeSpeaker, *fakeHints)
		stt    *fakeSTT
		code   string
		detail map[string]string
	}{
		{name: "all stages", detail: all(nil)},
		{name: "panel only", edit: func(e *EndToEnd, _ *fakeLLM, _ *fakeSpeaker, _ *fakeHints) { e.Hints = nil },
			detail: all(map[string]string{"pointing": skip})},
		{name: "nobody spoke", stt: &fakeSTT{}, code: CodeNoSpeech,
			detail: map[string]string{"speech": CodeNoSpeech, "stt": skip, "capture": skip, "llm": skip, "tts": skip, "pointing": skip}},
		{name: "wrong phrase", stt: &fakeSTT{final: speech("thanks for watching")}, code: CodeMismatch,
			detail: map[string]string{"speech": "ok", "stt": CodeMismatch, "capture": skip, "llm": skip, "tts": skip, "pointing": skip}},
		{name: "no capture", edit: func(e *EndToEnd, _ *fakeLLM, _ *fakeSpeaker, _ *fakeHints) { e.Capturer = nil },
			code:   readiness.PermUnavailable,
			detail: map[string]string{"speech": "ok", "stt": "ok", "capture": readiness.PermUnavailable, "llm": skip, "tts": skip, "pointing": skip}},
		{name: "model timeout", edit: func(_ *EndToEnd, p *fakeLLM, _ *fakeSpeaker, _ *fakeHints) { p.err = llm.ErrTimeout },
			code: "timeout", detail: all(map[string]string{"llm": "timeout", "tts": skip, "pointing": skip})},
		{name: "model fails mid-stream", edit: func(_ *EndToEnd, p *fakeLLM, _ *fakeSpeaker, _ *fakeHints) { p.failEv = llm.ErrAuth },
			code: "auth", detail: all(map[string]string{"llm": "auth", "tts": skip, "pointing": skip})},
		{name: "empty reply", edit: func(_ *EndToEnd, p *fakeLLM, _ *fakeSpeaker, _ *fakeHints) { p.reply = "  " },
			code: CodeEmptyReply, detail: all(map[string]string{"llm": CodeEmptyReply, "tts": skip, "pointing": skip})},
		{name: "no voice", edit: func(e *EndToEnd, _ *fakeLLM, _ *fakeSpeaker, _ *fakeHints) {
			e.Configured = func() bool { return false }
		},
			code: CodeNotConfigured, detail: all(map[string]string{"tts": CodeNotConfigured, "pointing": skip})},
		{name: "stale hint", edit: func(_ *EndToEnd, _ *fakeLLM, _ *fakeSpeaker, h *fakeHints) { h.err = contracts.ErrStale },
			code: "stale", detail: all(map[string]string{"pointing": "stale"})},
		{name: "hint fails", edit: func(_ *EndToEnd, _ *fakeLLM, _ *fakeSpeaker, h *fakeHints) { h.err = overlay.ErrInvalidCommand },
			code: CodeOverlayFailed, detail: all(map[string]string{"pointing": CodeOverlayFailed})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.stt
			if f == nil {
				f = &fakeSTT{final: speech(en)}
			}
			p, sp, h := &fakeLLM{reply: "I can see your screen."}, &fakeSpeaker{}, &fakeHints{}
			e := EndToEnd{
				Listener: listener(&fakeMic{frames: 20, hold: true}, f), Lang: func() string { return "en" },
				Capturer: &fakeCapturer{},
				Provider: func(context.Context) (llm.Provider, error) { return p, nil },
				Speaker:  sp, Configured: yes, Hints: h, HintTime: time.Millisecond,
			}
			if tt.edit != nil {
				tt.edit(&e, p, sp, h)
			}
			got := e.Run(context.Background())
			if got.Code != tt.code || (got.Status == readiness.Passed) != (tt.code == "") {
				t.Fatalf("got %s/%q, want %q (detail %v)", got.Status, got.Code, tt.code, got.Detail)
			}
			if !maps.Equal(got.Detail, tt.detail) {
				t.Errorf("detail = %v, want %v", got.Detail, tt.detail)
			}
			if got.Detail["llm"] == readiness.DetailOK {
				checkRequest(t, p.req)
			}
			if got.Detail["tts"] == readiness.DetailOK && (len(sp.said) != 1 || sp.said[0] != "I can see your screen.") {
				t.Errorf("spoke %q, want the reply", sp.said)
			}
			if e.Hints != nil && got.Detail["pointing"] != skip {
				if len(h.reg) != 1 || h.reg[0].Model.W == 0 {
					t.Errorf("registered %v, want the capture with its model size", h.reg)
				}
				if n := len(h.cmds); n == 0 || h.cmds[n-1].Op != overlay.OpClearSession {
					t.Errorf("commands %v: the hint must be cleared", h.cmds)
				}
			}
		})
	}
}

// The full test hands over what it heard, cleared at the start, so a
// mismatch shows the user what was recognised instead.
func TestEndToEndReportsWhatItHeard(t *testing.T) {
	tests := []struct {
		name string
		stt  *fakeSTT
		want []string
	}{
		{"mismatch", &fakeSTT{final: speech("thanks for watching")}, []string{"", "thanks for watching"}},
		{"nobody spoke", &fakeSTT{}, []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			e := EndToEnd{
				Listener: listener(&fakeMic{frames: 20, hold: true}, tt.stt), Lang: func() string { return "en" },
				Heard: func(s string) { got = append(got, s) },
			}
			e.Run(context.Background())
			if !slices.Equal(got, tt.want) {
				t.Errorf("heard %q, want %q", got, tt.want)
			}
		})
	}
}

// checkRequest: the request is the fixed prompt plus one screenshot.
func checkRequest(t *testing.T, req llm.ChatRequest) {
	t.Helper()
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 2 {
		t.Fatalf("request %+v", req.Messages)
	}
	c := req.Messages[0].Content
	if !strings.Contains(c[0].Text, "setup self-test") || c[1].Kind != llm.PartImage || len(c[1].Image.Data) == 0 {
		t.Errorf("request content %+v", c)
	}
}
