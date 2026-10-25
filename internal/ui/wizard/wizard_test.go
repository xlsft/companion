package wizard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/pkg/contracts"
)

func contains(s, sub string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(sub)) }

// allowedActions is everything the setup may ever offer (FR-ONB-004).
var allowedActions = []Action{ActBack, ActNext, ActOpen, ActApplyHotkeys, ActChoose, ActLanguage, ActReplaceKey,
	ActHeaders, ActModelList, ActRetryModel, ActVoiceCheck, ActReadSelf, ActRunFull, ActChangeKeys}

// forbiddenWords match at the start of a word, so "Скачать" is not "чат".
var forbiddenWords = []string{"skip", "later", "chat", "try without", "open chat", "пропуст", "позже", "чат", "без настройки"}

func offers(label, word string) bool {
	l := " " + strings.ToLower(label)
	for _, sep := range []string{" ", "(", "«", "\"", "/"} {
		if strings.Contains(l, sep+word) {
			return true
		}
	}
	return false
}

func assertNoBypass(t *testing.T, h *harness, where string) {
	t.Helper()
	for _, a := range h.w.frame.actions {
		if !slices.Contains(allowedActions, a) {
			t.Errorf("%s: unexpected action %q", where, a)
		}
	}
	for _, s := range h.w.frame.texts {
		for _, f := range forbiddenWords {
			if offers(s, f) {
				t.Errorf("%s: label %q offers %q", where, s, f)
			}
		}
	}
}

// Setup starts on the agent; Next moves on only once a screen passed,
// Back returns, and a reopened setup starts where work is left.
func TestScreensAndNavigation(t *testing.T) {
	h := newHarness(t)
	h.frame()
	if h.w.scr != screenAgent || h.has(ActBack) {
		t.Fatalf("start: screen %d, back %v", h.w.scr, h.has(ActBack))
	}
	h.click(ActNext)
	if h.w.scr != screenAgent {
		t.Fatal("Next left an unpassed agent")
	}
	h.pass(readiness.StepBYOK)
	h.frame()
	h.click(ActNext)
	if h.w.scr != screenVoice {
		t.Fatalf("Next after the agent passed: screen %d", h.w.scr)
	}
	h.click(ActBack)
	if h.w.scr != screenAgent {
		t.Fatalf("Back: screen %d", h.w.scr)
	}

	for _, tt := range []struct {
		name   string
		passed []readiness.Step
		want   screen
	}{
		{"fresh", nil, screenAgent},
		{"agent done", []readiness.Step{readiness.StepBYOK}, screenVoice},
		{"voice done", []readiness.Step{readiness.StepBYOK, readiness.StepTTS, readiness.StepSTT}, screenDone},
	} {
		h := newHarness(t)
		for _, st := range tt.passed {
			h.pass(st)
		}
		h.frame()
		if h.w.scr != tt.want {
			t.Errorf("%s: starts on %d, want %d", tt.name, h.w.scr, tt.want)
		}
	}
}

func TestNoForbiddenActions(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.RU} {
		h := newHarness(t)
		h.lang = lang
		for _, sc := range []screen{screenAgent, screenVoice, screenDone} {
			h.goScreen(sc)
			assertNoBypass(t, h, fmt.Sprintf("%s/%d/fresh", lang, sc))
			for _, st := range readiness.Steps {
				h.checks.set(st, readiness.Result{Status: readiness.Failed, Code: "denied",
					Detail: map[string]string{"global_hotkey": readiness.PermNeedsBinding, readiness.DetailSpoke: "auth"}})
				_, _ = h.gate.Run(context.Background(), st)
			}
			h.w.langDD.SetOpen(true)
			h.w.stt.deviceDD.SetOpen(true)
			h.frame()
			assertNoBypass(t, h, fmt.Sprintf("%s/%d/failed", lang, sc))
			h.w.langDD.SetOpen(false)
			h.w.stt.deviceDD.SetOpen(false)
		}
	}
}

// No technical code or subsystem name reaches the user: a failure shows
// what to do, in words.
func TestNoTechnicalCodes(t *testing.T) {
	h := newHarness(t)
	h.checks.set(readiness.StepBYOK, readiness.Result{Status: readiness.Failed, Code: "auth"})
	h.provider.info = &providers.Info{ID: "p", HasKey: true, Input: providers.Input{Kind: llm.KindOpenAI, BaseURL: "https://api.openai.com/v1", Model: "m"}}
	h.goScreen(screenAgent)
	h.eventually("loaded", func() bool { return h.w.byok.loaded })
	if _, err := h.gate.Run(context.Background(), readiness.StepBYOK); err != nil {
		t.Fatal(err)
	}
	h.eventually("failure", func() bool { return h.shows(fixText(h.lang, "auth")) })
	for _, s := range h.w.frame.texts {
		for _, code := range []string{"(auth)", "Status:", "Subsystem", "auth_model"} {
			if strings.Contains(s, code) {
				t.Errorf("technical text on screen: %q", s)
			}
		}
	}
}

// A provider tile fills the endpoint, never the model; the custom server
// is the only one with an address and an API choice.
func TestAgentTiles(t *testing.T) {
	h := newHarness(t)
	h.eventually("agent", func() bool { return h.w.byok.loaded })
	b := &h.w.byok
	h.pickAgent("anthropic")
	if b.kind.Value != string(llm.KindAnthropic) || b.baseURL.Text() != "https://api.anthropic.com" || b.model.Text() != "" {
		t.Errorf("anthropic: kind %q url %q model %q", b.kind.Value, b.baseURL.Text(), b.model.Text())
	}
	if h.shows(h.tr("wiz.byok.baseURL")) || h.shows(h.tr("wiz.agent.speaks")) {
		t.Error("a preset shows the address or the API choice")
	}
	if !h.shows(i18n.Tf(h.lang, "wiz.agent.keyFor", "Anthropic")) {
		t.Error("no key field named after the service")
	}
	h.pickAgent(customTile)
	if b.baseURL.Text() != "" || !h.shows(h.tr("wiz.byok.baseURL")) || !h.shows(h.tr("wiz.agent.speaks")) {
		t.Errorf("custom: url %q, fields shown %v", b.baseURL.Text(), h.shows(h.tr("wiz.byok.baseURL")))
	}
}

// The form saves itself once complete and the user paused; the key never
// shows again, and the connection check runs by itself.
func TestAgentSavesItselfAndChecks(t *testing.T) {
	h := newHarness(t)
	h.eventually("agent", func() bool { return h.w.byok.loaded })
	b := &h.w.byok
	h.pickAgent("openai")
	const secret = "sk-test-secret-123"
	b.key.SetText(secret)
	b.model.SetText("gpt-test")
	h.checks.set(readiness.StepBYOK, readiness.Result{Status: readiness.Passed})
	h.eventually("saved", func() bool { return len(h.provider.savedKeys()) == 1 })
	h.eventually("checked", func() bool { return h.checks.count(readiness.StepBYOK) == 1 && h.idle() })
	if k := h.provider.savedKeys(); k[0] != secret {
		t.Errorf("saved key %q", k[0])
	}
	for i := 0; i < 3; i++ {
		h.frame()
		for _, s := range h.w.frame.texts {
			if strings.Contains(s, secret) {
				t.Fatalf("key on screen: %q", s)
			}
		}
	}
	if !h.shows(h.tr("wiz.agent.connected")) {
		t.Error("no connected line after a passed check")
	}
}

// The recommended speech model downloads by itself as setup opens; its
// chip goes away a few seconds after it is ready.
func TestSpeechModelDownloadsByItself(t *testing.T) {
	h := newHarness(t)
	h.eventually("installed", func() bool {
		inst, _ := h.stt.Installed()
		return len(inst) == 1
	})
	h.eventually("ready chip", func() bool { return h.shows(h.tr("wiz.model.ready")) })
	h.w.model.set(func() { h.w.model.readyAt = time.Now().Add(-2 * modelReadyFor) })
	h.frame()
	if h.shows(h.tr("wiz.model.ready")) {
		t.Error("ready chip still shown")
	}
	cat, _ := models.BuiltinCatalog()
	inst, _ := h.stt.Installed()
	if _, ok := cat.Find(inst[0].ID); !ok {
		t.Errorf("installed %q is not a catalogue model", inst[0].ID)
	}
}

// The voice check speaks and listens (StepTTS); when the microphone heard
// it, StepSTT passes on that recording. When it did not, the user can read
// the phrase instead.
func TestVoiceCheck(t *testing.T) {
	for _, tt := range []struct {
		name  string
		heard string
	}{{"heard", readiness.DetailOK}, {"headphones", readiness.HeardNoSpeech}} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.checks.set(readiness.StepTTS, readiness.Result{Status: readiness.Passed,
				Detail: map[string]string{readiness.DetailSpoke: readiness.DetailOK, readiness.DetailHeard: tt.heard}})
			h.checks.set(readiness.StepSTT, readiness.Result{Status: readiness.Passed})
			h.goScreen(screenVoice)
			h.eventually("voice check ran", func() bool { return h.checks.count(readiness.StepTTS) == 1 && h.idle() })
			h.frame()
			if tt.heard == readiness.DetailOK {
				h.eventually("recognition passed", func() bool { return h.checks.count(readiness.StepSTT) == 1 })
				h.eventually("works", func() bool { return h.shows(h.tr("wiz.voice.works")) })
				return
			}
			if h.checks.count(readiness.StepSTT) != 0 || !h.has(ActReadSelf) {
				t.Fatalf("not heard: STT runs %d, read-self offered %v", h.checks.count(readiness.StepSTT), h.has(ActReadSelf))
			}
			h.click(ActReadSelf)
			h.eventually("read self", func() bool { return h.checks.count(readiness.StepSTT) == 1 && h.idle() })
		})
	}
}

// The voice screen starts on Edge (no key) and shows no key field for it.
func TestVoiceStartsOnEdge(t *testing.T) {
	h := newHarness(t)
	h.tts.cfg = TTSConfig{Provider: tts.EdgeProviderID, Speed: 1}
	h.goScreen(screenVoice)
	h.eventually("loaded", func() bool { return h.w.tts.loaded && h.w.tts.tile.Value == tts.EdgeProviderID })
	if h.shows(h.tr("wiz.tts.key")) {
		t.Error("key field for Edge")
	}
	h.pickVoice("openai")
	if !h.shows(h.tr("wiz.tts.key")) {
		t.Error("no key field for OpenAI")
	}
}

// Picking a local speech server fills its address; ElevenLabs drops it.
func TestVoiceServerTiles(t *testing.T) {
	h := newHarness(t)
	h.goScreen(screenVoice)
	ts := &h.w.tts
	h.pickVoice("kokoro")
	if ts.endpoint() != "http://localhost:8880/v1" || ts.model.Text() != "" {
		t.Errorf("kokoro: %q model %q", ts.endpoint(), ts.model.Text())
	}
	h.pickVoice("elevenlabs")
	if ts.endpoint() != "" {
		t.Errorf("ElevenLabs keeps an endpoint: %q", ts.endpoint())
	}
}

// The finish runs the full test by itself once everything else passed,
// and opens Companion only on the user's click, once.
func TestDoneRunsFullTestAndOpens(t *testing.T) {
	h := newHarness(t)
	for _, st := range readiness.Steps[:len(readiness.Steps)-1] {
		h.pass(st)
	}
	h.checks.set(readiness.StepEndToEnd, readiness.Result{Status: readiness.Passed})
	h.frame()
	if h.w.scr != screenDone {
		t.Fatalf("screen %d", h.w.scr)
	}
	h.eventually("full test", func() bool { return h.checks.count(readiness.StepEndToEnd) == 1 && h.idle() })
	h.eventually("ready", func() bool { return h.w.snap().prog.State == readiness.Ready })
	if h.ready.Load() != 0 {
		t.Fatal("opened Companion without a click")
	}
	h.click(ActOpen)
	h.eventually("opened", func() bool { return h.ready.Load() == 1 })
	h.click(ActOpen)
	time.Sleep(20 * time.Millisecond)
	if h.ready.Load() != 1 {
		t.Errorf("OnReady ran %d times", h.ready.Load())
	}
}

// Permissions are checked in the background; one that needs the user
// shows on the finish with what to do, shortcuts with their editor.
func TestDoneShowsPermissionIssue(t *testing.T) {
	h := newHarness(t)
	h.checks.set(readiness.StepPermissions, readiness.Result{Status: readiness.Failed, Code: readiness.PermNeedsBinding,
		Detail: map[string]string{"microphone": readiness.PermGranted, "global_hotkey": readiness.PermNeedsBinding}})
	h.goScreen(screenDone)
	h.eventually("background check", func() bool { return h.checks.count(readiness.StepPermissions) >= 1 })
	h.eventually("issue", func() bool { return h.shows(h.tr("wiz.perm.global_hotkey")) && h.has(ActApplyHotkeys) })
	if h.shows(h.tr("wiz.perm.microphone")) {
		t.Error("a working permission listed as an issue")
	}
}

// The double press is a key picker where the system can see a lone key,
// and plainly unavailable elsewhere.
func TestDoneDoubleTap(t *testing.T) {
	tests := []struct {
		name      string
		supported bool
		err       error
		wantSet   bool
		wantKey   string
		wantShown string
	}{
		{"unavailable", false, nil, false, "", "wiz.perm.status.unavailable"},
		{"set ctrl", true, nil, true, "ctrl", "wiz.done.double"},
		{"refused", true, contracts.Unsupported(contracts.CapDoubleTap), true, "", "wiz.perm.status.unavailable"},
		{"failed", true, errors.New("boom"), true, "", "wiz.done.double.failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.perm.mu.Lock()
			h.perm.tapSupported, h.perm.tapErr = tt.supported, tt.err
			h.perm.mu.Unlock()
			h.goScreen(screenDone)
			h.frame()
			if tt.wantSet {
				h.w.tap.dd.SetOpen(true)
				h.frame()
				h.w.tap.dd.Pick(1) // Ctrl
				h.frame()
				h.eventually("saved", func() bool {
					h.w.mu.Lock()
					defer h.w.mu.Unlock()
					return len(h.perm.taps()) == 1 && !h.w.tap.saving
				})
				if got := h.perm.taps()[0]; got != "ctrl" {
					t.Errorf("set %q, want ctrl", got)
				}
				h.frame()
			}
			if !h.shows(h.tr(tt.wantShown)) {
				t.Errorf("%s not shown", tt.wantShown)
			}
			h.w.mu.Lock()
			key := h.w.tap.key
			h.w.mu.Unlock()
			if key != tt.wantKey {
				t.Errorf("key %q, want %q", key, tt.wantKey)
			}
			if tt.wantKey != "" && !h.shows("Ctrl") {
				t.Error("chosen key not shown")
			}
		})
	}
}

// Hundreds of voices (Edge) are a dropdown; picking one marks the form
// edited.
func TestManyVoicesAreADropdown(t *testing.T) {
	h := newHarness(t)
	var many []tts.Voice
	for i := range 20 {
		id := fmt.Sprintf("v%02d", i)
		many = append(many, tts.Voice{ID: id, Name: "Voice " + id, Languages: []string{"en-US"}})
	}
	h.tts.mu.Lock()
	h.tts.voices = many
	h.tts.cfg = TTSConfig{Provider: tts.EdgeProviderID, Speed: 1}
	h.tts.mu.Unlock()
	h.goScreen(screenVoice)
	ts := &h.w.tts
	h.eventually("voices", func() bool {
		h.w.mu.Lock()
		defer h.w.mu.Unlock()
		return len(ts.voices) == 20
	})
	ts.voiceDD.SetOpen(true)
	h.frame()
	ts.voiceDD.Pick(5)
	h.frame()
	h.frame()
	h.w.mu.Lock()
	got := ts.voice
	h.w.mu.Unlock()
	if got != "v05" || !ts.dirty {
		t.Errorf("voice = %q, dirty %v", got, ts.dirty)
	}
}

// Leaving a screen cancels its work, so nothing keeps running unseen.
func TestLeavingScreenCancelsWork(t *testing.T) {
	h := newHarness(t)
	h.pass(readiness.StepBYOK)
	h.checks.mu.Lock()
	h.checks.block[readiness.StepEndToEnd] = true
	h.checks.mu.Unlock()
	for _, st := range readiness.Steps[:len(readiness.Steps)-1] {
		h.pass(st)
	}
	h.frame()
	h.eventually("running", func() bool { return h.checks.count(readiness.StepEndToEnd) == 1 })
	h.w.enterScreen(screenVoice)
	h.eventually("cancelled", h.idle)
}
