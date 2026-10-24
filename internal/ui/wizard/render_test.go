package wizard

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gioui.org/gpu/headless"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/llm/probe"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/theme"
)

// TestHeadlessRender draws every step through Gio's GPU pipeline when an
// offscreen GL context is available. Set OC_UI_SNAPSHOT_DIR to keep PNGs.
func TestHeadlessRender(t *testing.T) {
	size := image.Pt(1280, 800) // the prototypes' frame
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skipf("no headless GPU context on this machine: %v", err)
	}
	defer win.Release()
	dir := os.Getenv("OC_UI_SNAPSHOT_DIR")

	tr := providers.TestResult{ProviderID: "prov_1", Steps: []providers.StepResult{
		{ID: providers.StepConnection, Status: providers.StatusOK, MessageKey: "provider.connection.ok"},
		{ID: providers.StepAuthModel, Status: providers.StatusFailed, MessageKey: "provider.error.auth", Code: "auth"},
		{ID: providers.StepCapabilities, Status: providers.StatusSkipped, MessageKey: "provider.skipped"},
	}, Capabilities: &probe.Report{}}
	failures := map[readiness.Step]readiness.Result{
		readiness.StepPermissions: {Status: readiness.Failed, Code: "needs_compositor_binding", Detail: map[string]string{
			"microphone": readiness.PermGranted, "screen_capture": readiness.PermGranted,
			"global_hotkey": readiness.PermNeedsBinding, "overlay_click_through": readiness.PermFallback}},
		readiness.StepBYOK:     {Status: readiness.Failed, Code: "auth", Detail: tr.Detail()},
		readiness.StepSTT:      {Status: readiness.Failed, Code: "mismatch"},
		readiness.StepTTS:      {Status: readiness.Failed, Code: "playback_failed"},
		readiness.StepPlatform: {Status: readiness.Failed, Code: "unavailable"},
		readiness.StepEndToEnd: {Status: readiness.Failed, Code: "llm_failed", Detail: map[string]string{
			"speech": "ok", "stt": "ok", "capture": "ok", "llm": "timeout", "tts": "skipped", "pointing": "skipped"}},
	}
	for _, lang := range []i18n.Lang{i18n.EN, i18n.RU} {
		h := newHarness(t)
		h.lang, h.size = lang, size
		h.w.cfg.Version, h.w.cfg.SetLanguage = "0.1.0-dev", func(i18n.Lang) {}
		h.provider.info = &providers.Info{ID: "prov_1", HasKey: true,
			Input: providers.Input{Kind: llm.KindOpenAI, BaseURL: "https://api.example.test/v1", Model: "gpt-test"}}
		for _, st := range readiness.Steps {
			h.eventually("step", func() bool { return h.view() == st })
			h.checks.set(st, failures[st])
			if _, err := h.gate.Run(t.Context(), st); err != nil {
				t.Fatal(err)
			}
			h.eventually("failure", func() bool { return h.shows(i18n.T(lang, "wiz.result.failed")) })
			for _, sc := range []struct {
				suffix string
				th     *theme.Theme
			}{{"", theme.New(false, 1)}, {"-dark", theme.New(true, 1)}, {"-dark-x2", theme.New(true, theme.MaxScale)}} {
				h.th = sc.th
				h.frame()
				ops := h.frame()
				if err := win.Frame(ops); err != nil {
					t.Fatalf("%s: frame: %v", st, err)
				}
				img := image.NewRGBA(image.Rectangle{Max: size})
				if err := win.Screenshot(img); err != nil {
					t.Fatalf("%s: screenshot: %v", st, err)
				}
				if dir != "" {
					f, err := os.Create(filepath.Join(dir, "wizard-"+string(lang)+"-"+string(st)+sc.suffix+".png"))
					if err != nil {
						t.Fatal(err)
					}
					if err := png.Encode(f, img); err != nil {
						t.Fatal(err)
					}
					f.Close()
				}
			}
			h.th = theme.New(false, 1)
			if st != readiness.StepEndToEnd {
				h.pass(st)
			}
		}
	}
}
