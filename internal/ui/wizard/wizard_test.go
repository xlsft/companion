package wizard

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/llm/probe"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/theme"
)

func contains(s, sub string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(sub)) }

func TestStepLocking(t *testing.T) {
	h := newHarness(t)
	h.frame()
	if h.view() != readiness.StepPermissions {
		t.Fatalf("first step = %s", h.view())
	}
	// Only the first step is selectable; the rest are visible but locked.
	n := 0
	for _, a := range h.w.frame.actions {
		if a == ActSelectStep {
			n++
		}
	}
	if n != 1 {
		t.Errorf("selectable steps = %d, want 1", n)
	}
	for _, st := range readiness.Steps {
		if !h.shows(i18n.T(i18n.EN, "wiz.step."+string(st))) {
			t.Errorf("step %s not listed", st)
		}
	}
	if !h.shows("locked") {
		t.Error("locked state not shown")
	}
	// Jumping ahead does nothing.
	h.w.stepBtns[3].Click()
	h.frame()
	h.frame()
	if h.view() != readiness.StepPermissions {
		t.Errorf("jumped ahead to %s", h.view())
	}
	if h.has(ActBack) {
		t.Error("Back offered on the first step")
	}
	// Next is shown but leads nowhere before the step passes.
	h.click(ActNext)
	h.frame()
	if h.view() != readiness.StepPermissions {
		t.Errorf("Next moved past an unpassed step to %s", h.view())
	}

	// Passing a step advances; Back returns; the sidebar goes forward again.
	h.checks.set(readiness.StepPermissions, readiness.Result{Status: readiness.Passed})
	h.click(ActCheck)
	h.eventually("advance to BYOK", func() bool { return h.view() == readiness.StepBYOK && h.idle() })
	h.click(ActBack)
	h.eventually("back to permissions", func() bool { return h.view() == readiness.StepPermissions })
	h.click(ActNext)
	h.eventually("forward to BYOK", func() bool { return h.view() == readiness.StepBYOK })
	h.w.stepBtns[2].Click()
	h.frame()
	h.frame()
	if h.view() != readiness.StepBYOK {
		t.Errorf("unpassed BYOK let the user reach %s", h.view())
	}
}

// allowedActions is everything the wizard may ever offer (FR-ONB-004).
var allowedActions = []Action{ActBack, ActCheck, ActRecheck, ActDiagnostics, ActQuit, ActSelectStep, ActApplyHotkeys,
	ActSave, ActTest, ActDownload, ActCancel, ActMicTest, ActLoadVoices, ActChoose, ActRunFull,
	ActNext, ActLanguage, ActReplaceKey, ActHeaders}

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
	for _, a := range []Action{ActDiagnostics, ActQuit} {
		if !h.has(a) {
			t.Errorf("%s: %s missing", where, a)
		}
	}
}

func TestNoForbiddenActions(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.RU} {
		h := newHarness(t)
		h.lang = lang
		for _, st := range readiness.Steps {
			h.eventually("step "+string(st), func() bool { return h.view() == st })
			// Fresh, failed and (for the menus) expanded states.
			assertNoBypass(t, h, string(lang)+"/"+string(st)+"/fresh")
			h.checks.set(st, readiness.Result{Status: readiness.Failed, Code: "denied",
				Detail: map[string]string{"global_hotkey": readiness.PermNeedsBinding, "speech": "no_speech"}})
			if _, err := h.gate.Run(context.Background(), st); err != nil {
				t.Fatal(err)
			}
			h.eventually("failure shown", func() bool { return h.shows(i18n.T(lang, "wiz.result.failed")) })
			h.w.stt.modelDD.SetOpen(true)
			h.w.stt.deviceDD.SetOpen(true)
			h.w.langDD.SetOpen(true)
			h.frame()
			assertNoBypass(t, h, string(lang)+"/"+string(st)+"/failed")
			if st == readiness.StepEndToEnd {
				break
			}
			h.pass(st)
		}
	}
}

func TestFailureKeepsStepAndExplains(t *testing.T) {
	h := newHarness(t)
	h.checks.set(readiness.StepPermissions, readiness.Result{Status: readiness.Failed, Code: "denied",
		Detail: map[string]string{"microphone": readiness.PermDenied, "screen_capture": readiness.PermGranted,
			"global_hotkey": readiness.PermNeedsBinding, "overlay_click_through": readiness.PermFallback}})
	h.frame()
	h.click(ActCheck)
	h.eventually("failure", func() bool { return h.idle() && h.shows("Check failed") })
	if h.view() != readiness.StepPermissions {
		t.Fatalf("failure moved to %s", h.view())
	}
	for _, want := range []string{
		"Subsystem: permissions", "Status: denied", "How to fix:",
		"Denied", "Works", "Action needed", "Fallback",
		"Talk", "Cancel", "Super", "Space", "Backspace", "Apply",
	} {
		if !h.shows(want) {
			t.Errorf("missing %q", want)
		}
	}
	if !h.has(ActRecheck) || !h.has(ActApplyHotkeys) {
		t.Errorf("actions = %v", h.w.frame.actions)
	}
}

func TestOnReadyCalledOnce(t *testing.T) {
	h := newHarness(t)
	h.checks.passAll()
	for _, st := range readiness.Steps[:len(readiness.Steps)-1] {
		h.pass(st)
	}
	h.eventually("end-to-end step", func() bool { return h.view() == readiness.StepEndToEnd })
	if h.ready.Load() != 0 {
		t.Fatal("ready before the end-to-end test")
	}
	h.click(ActRunFull)
	h.eventually("ready", func() bool { return h.ready.Load() == 1 })
	for range 5 {
		h.click(ActRunFull)
	}
	if _, err := h.gate.Run(context.Background(), readiness.StepEndToEnd); err != nil {
		t.Fatal(err)
	}
	h.frame()
	if n := h.ready.Load(); n != 1 {
		t.Errorf("OnReady called %d times", n)
	}
}

func TestEndToEndStagesShownSeparately(t *testing.T) {
	h := newHarness(t)
	for _, st := range readiness.Steps[:5] {
		h.pass(st)
	}
	h.checks.set(readiness.StepEndToEnd, readiness.Result{Status: readiness.Failed, Code: "llm_failed",
		Detail: map[string]string{"speech": "ok", "stt": "ok", "capture": "ok", "llm": "timeout", "tts": "skipped", "pointing": "skipped"}})
	h.eventually("e2e", func() bool { return h.view() == readiness.StepEndToEnd })
	h.click(ActRunFull)
	h.eventually("result", func() bool { return h.idle() && h.shows("Status: llm_failed") })
	for _, want := range []string{"Speech — Done", "Local STT — Done", "Screenshot — Done",
		"Model answer — Failed (timeout)", "Speech output — Not run", "Pointing — Not run"} {
		if !h.shows(want) {
			t.Errorf("missing %q in %q", want, h.w.frame.texts)
		}
	}
	if h.ready.Load() != 0 || h.gate.IsReady() {
		t.Error("failed end-to-end made the app ready")
	}
}

func TestKeyNeverRenderedAfterSave(t *testing.T) {
	const secret = "sk-test-SECRET-4242"
	h := newHarness(t)
	h.pass(readiness.StepPermissions)
	h.eventually("byok", func() bool { return h.view() == readiness.StepBYOK && h.w.byok.loaded })
	b := &h.w.byok
	b.baseURL.SetText("https://llm.example.test/v1")
	b.model.SetText("test-model")
	b.key.SetText(secret)
	b.hdr.SetText("X-Org: acme\n\n")
	h.frame()
	if h.shows(secret) {
		t.Fatal("key shown in a label while typing")
	}
	h.click(ActSave)
	if b.key.Text() != "" {
		t.Error("key still in the editor after Save")
	}
	h.eventually("saved", func() bool { return h.shows("Saved in the system keychain") })
	if got := h.provider.savedKeys(); !slices.Equal(got, []string{secret}) {
		t.Errorf("backend keys = %v", got)
	}
	if !h.shows("go only to llm.example.test") {
		t.Errorf("endpoint in use not shown: %q", h.w.frame.texts)
	}
	for _, s := range h.w.frame.texts {
		if strings.Contains(s, secret) {
			t.Errorf("secret rendered in %q", s)
		}
	}
	// Re-saving without a key keeps it (empty APIKey on Update).
	h.click(ActSave)
	h.eventually("resaved", func() bool { return h.shows("Saved in the system keychain") && !h.w.byok.saving })
	if got := h.provider.savedKeys(); len(got) != 1 {
		t.Errorf("backend keys after resave = %v", got)
	}
	if h.provider.info.Input.Headers["X-Org"] != "acme" {
		t.Errorf("headers = %v", h.provider.info.Input.Headers)
	}

	// The TTS key follows the same rule.
	h.pass(readiness.StepBYOK)
	h.pass(readiness.StepSTT)
	h.eventually("tts", func() bool { return h.view() == readiness.StepTTS })
	h.w.tts.key.SetText(secret)
	h.frame()
	h.click(ActSave)
	if h.w.tts.key.Text() != "" {
		t.Error("TTS key still in the editor after Save")
	}
	h.eventually("tts saved", func() bool { return h.shows("Saved in the system keychain") })
	for _, s := range h.w.frame.texts {
		if strings.Contains(s, secret) {
			t.Errorf("secret rendered in %q", s)
		}
	}
}

func TestBYOKTestSteps(t *testing.T) {
	h := newHarness(t)
	h.pass(readiness.StepPermissions)
	h.provider.info = &providers.Info{ID: "prov_1", HasKey: true,
		Input: providers.Input{Kind: llm.KindAnthropic, BaseURL: "https://api.example.test", Model: "m1"}}
	tr := providers.TestResult{ProviderID: "prov_1", Steps: []providers.StepResult{
		{ID: providers.StepConnection, Status: providers.StatusOK, MessageKey: "provider.connection.ok"},
		{ID: providers.StepAuthModel, Status: providers.StatusOK, MessageKey: "provider.auth_model.ok"},
		{ID: providers.StepCapabilities, Status: providers.StatusOK, MessageKey: "provider.capabilities.ok"},
	}, Capabilities: &probe.Report{Results: []probe.Result{
		{Capability: probe.CapText, OK: true}, {Capability: probe.CapStream, OK: true},
		{Capability: llm.CapVision, OK: false, ErrCode: "wrong_answer"}, {Capability: llm.CapTools, OK: true},
	}}}
	h.checks.set(readiness.StepBYOK, readiness.Result{Status: readiness.Passed, Detail: tr.Detail()})
	// A stored provider is tested on arrival, without a click.
	h.eventually("tested", func() bool { return h.view() == readiness.StepSTT })
	if n := h.checks.count(readiness.StepBYOK); n != 1 {
		t.Errorf("BYOK checks = %d, want 1", n)
	}
	h.w.stepBtns[1].Click()
	h.eventually("back to byok", func() bool { return h.view() == readiness.StepBYOK && h.w.byok.baseURL.Text() != "" })
	if h.w.byok.kind.Value != string(llm.KindAnthropic) || h.w.byok.dirty {
		t.Errorf("prefill kind=%s dirty=%v", h.w.byok.kind.Value, h.w.byok.dirty)
	}
	for _, want := range []string{"Connection — The endpoint answers.", "Key and model — The key is accepted",
		"Text — yes", "Streaming — yes", "Vision — no", "Tools — yes"} {
		if !h.shows(want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestProgressFromSubscribe(t *testing.T) {
	h := newHarness(t)
	h.frame()
	// A check run elsewhere (not by a click) reaches the wizard.
	h.checks.set(readiness.StepPermissions, readiness.Result{Status: readiness.Failed, Code: "unavailable"})
	if _, err := h.gate.Run(context.Background(), readiness.StepPermissions); err != nil {
		t.Fatal(err)
	}
	h.eventually("failure via subscribe", func() bool { return h.shows("Status: unavailable") })
	h.pass(readiness.StepPermissions)
	h.eventually("advance via subscribe", func() bool { return h.view() == readiness.StepBYOK })
	if !h.shows("passed") {
		t.Error("sidebar does not show the passed step")
	}
}

func TestLeavingStepCancelsWork(t *testing.T) {
	h := newHarness(t)
	h.pass(readiness.StepPermissions)
	h.pass(readiness.StepBYOK)
	h.stt.installBlocks = true
	h.eventually("stt loaded", func() bool { return h.view() == readiness.StepSTT && h.has(ActDownload) })
	h.click(ActDownload)
	h.eventually("downloading", func() bool { return h.has(ActCancel) && h.shows("MB/s") })
	h.click(ActBack)
	h.eventually("install cancelled", func() bool { return h.stt.canceled.Load() })

	// A running check is cancelled by Close as well.
	h2 := newHarness(t)
	h2.checks.block[readiness.StepPermissions] = true
	h2.frame()
	h2.click(ActCheck)
	h2.eventually("running", func() bool { return h2.checks.count(readiness.StepPermissions) == 1 })
	h2.w.Close()
	h2.eventually("check ended", h2.idle)
	if r := h2.gate.Progress().Results[readiness.StepPermissions]; r.Status == readiness.Passed {
		t.Errorf("cancelled check result = %+v", r)
	}
}

func TestSTTFlow(t *testing.T) {
	h := newHarness(t)
	h.lang = i18n.RU
	h.pass(readiness.StepPermissions)
	h.pass(readiness.StepBYOK)
	h.eventually("stt loaded", func() bool { return h.view() == readiness.StepSTT && h.has(ActDownload) })
	if !h.shows("ГБ ОЗУ") || !h.shows("Рекомендуем") || !h.shows("Parakeet") {
		t.Errorf("hardware/recommendation missing: %q", h.w.frame.texts)
	}
	if !h.shows("«Привет, это проверка микрофона»") {
		t.Error("RU phrase missing")
	}
	h.click(ActDownload)
	h.eventually("installed", func() bool { return h.shows("скачана") })
	// Pick the second microphone and watch the level.
	h.w.stt.deviceDD.Pick(2)
	h.frame()
	h.click(ActMicTest)
	h.eventually("meter", func() bool { return h.shows("дБFS") })
	h.checks.set(readiness.StepSTT, readiness.Result{Status: readiness.Failed, Code: "mismatch"})
	h.click(ActCheck)
	h.eventually("checked", func() bool { return h.idle() && h.shows("Распознано:") })
	if h.stt.device != "mic2" {
		t.Errorf("selected device = %q", h.stt.device)
	}
	if h.w.stt.meter != nil {
		t.Error("meter still open after the check")
	}
	if !h.shows("Статус: mismatch") || h.view() != readiness.StepSTT {
		t.Error("STT failure not kept on the step")
	}
}

func TestDiagnosticsAndQuit(t *testing.T) {
	h := newHarness(t)
	h.frame()
	h.click(ActDiagnostics)
	h.click(ActQuit)
	h.eventually("callbacks", func() bool { return h.diag.Load() == 1 && h.quit.Load() == 1 })
	if h.view() != readiness.StepPermissions || h.gate.IsReady() {
		t.Error("Diagnostics or Quit changed setup state")
	}
}

// TestLayoutAllSteps renders every step in both languages, both themes and
// the text scale extremes without a display.
func TestLayoutAllSteps(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.RU, i18n.EN} {
		for _, dark := range []bool{false, true} {
			for _, scale := range []float32{theme.MinScale, 1, theme.MaxScale} {
				h := newHarness(t)
				h.lang, h.th = lang, theme.New(dark, scale)
				h.size.Y = 700 // a real window: the list scrolls
				for i, st := range readiness.Steps {
					h.eventually("step", func() bool { return h.view() == st })
					h.frame()
					h.w.stt.modelDD.SetOpen(true)
					h.w.langDD.SetOpen(true)
					h.frame()
					if i < len(readiness.Steps)-1 {
						h.pass(st)
					}
				}
			}
		}
	}
}

// TestAutoChecks: arriving at a step starts its check without a click, and
// only the steps that need nothing from the user do so.
func TestAutoChecks(t *testing.T) {
	h := newHarness(t)
	h.eventually("permissions auto-run", func() bool { return h.checks.count(readiness.StepPermissions) >= 1 && h.idle() })
	n := h.checks.count(readiness.StepPermissions)
	for range 5 {
		h.frame()
	}
	if got := h.checks.count(readiness.StepPermissions); got != n {
		t.Errorf("permissions ran %d times across idle frames, want %d", got, n)
	}

	h.checks.set(readiness.StepPermissions, readiness.Result{Status: readiness.Passed})
	h.click(ActRecheck)
	h.eventually("byok", func() bool { return h.view() == readiness.StepBYOK })
	// No provider saved: nothing to test yet.
	for range 3 {
		h.frame()
	}
	if c := h.checks.count(readiness.StepBYOK); c != 0 {
		t.Errorf("BYOK ran %d times without a saved provider", c)
	}

	h.pass(readiness.StepBYOK)
	h.eventually("stt", func() bool { return h.view() == readiness.StepSTT })
	for range 5 {
		h.frame()
	}
	if c := h.checks.count(readiness.StepSTT); c != 0 {
		t.Errorf("STT recorded by itself %d times", c)
	}
	if h.has(ActRecheck) && !h.w.snap().prog.Results[readiness.StepSTT].At.IsZero() {
		t.Error("STT has a result before the user started")
	}

	h.pass(readiness.StepSTT)
	h.eventually("tts", func() bool { return h.view() == readiness.StepTTS })
	for range 3 {
		h.frame()
	}
	if c := h.checks.count(readiness.StepTTS); c != 0 {
		t.Errorf("TTS played %d times before Save", c)
	}
	h.click(ActSave)
	h.eventually("tts auto after save", func() bool { return h.checks.count(readiness.StepTTS) == 1 && h.idle() })

	h.pass(readiness.StepTTS)
	h.eventually("platform", func() bool { return h.view() == readiness.StepPlatform })
	h.eventually("platform auto", func() bool { return h.checks.count(readiness.StepPlatform) >= 1 })
	h.pass(readiness.StepPlatform)
	h.eventually("e2e", func() bool { return h.view() == readiness.StepEndToEnd })
	for range 5 {
		h.frame()
	}
	if c := h.checks.count(readiness.StepEndToEnd); c != 0 {
		t.Errorf("end-to-end ran by itself %d times", c)
	}
}

// TestPermissionsRetryWhileFailing: a failing permissions check repeats on
// its own so a fix in the system settings turns the row green.
func TestPermissionsRetryWhileFailing(t *testing.T) {
	h := newHarness(t)
	h.eventually("first run", func() bool { return h.checks.count(readiness.StepPermissions) == 1 && h.idle() })
	h.checks.set(readiness.StepPermissions, readiness.Result{Status: readiness.Passed})
	// Pretend the last run was long ago instead of sleeping.
	rec := h.w.auto[readiness.StepPermissions]
	rec.at = rec.at.Add(-2 * autoRetry)
	h.w.auto[readiness.StepPermissions] = rec
	h.eventually("advance after the fix", func() bool { return h.view() == readiness.StepBYOK })
	if c := h.checks.count(readiness.StepPermissions); c != 2 {
		t.Errorf("permissions ran %d times, want 2", c)
	}
}

// A failing check is repeated every few seconds; its card must stay up
// while the next run is in flight instead of blinking.
func TestFailureStaysWhileRechecking(t *testing.T) {
	for _, running := range []bool{false, true} {
		h := newHarness(t)
		h.eventually("permissions step", func() bool { return h.view() == readiness.StepPermissions })
		h.checks.set(readiness.StepPermissions, readiness.Result{Status: readiness.Failed, Code: "denied"})
		if _, err := h.gate.Run(t.Context(), readiness.StepPermissions); err != nil {
			t.Fatal(err)
		}
		h.eventually("failure", func() bool { return h.shows(i18n.T(i18n.EN, "wiz.result.failed")) })
		h.w.mu.Lock()
		h.w.running = running
		h.w.mu.Unlock()
		h.frame()
		if !h.shows(i18n.T(i18n.EN, "wiz.result.failed")) {
			t.Errorf("running=%v: failure card hidden", running)
		}
		h.w.mu.Lock()
		h.w.running = false
		h.w.mu.Unlock()
	}
}
