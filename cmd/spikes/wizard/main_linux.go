//go:build linux && cgo

// Command wizard opens the real setup window at any step so every screen
// can be reviewed and screenshotted before the steps' backends are wired
// (OC-70). Earlier steps are passed through a real readiness Gate kept in
// memory; the shown step is fresh or carries a typical failure. Hardware,
// microphones and the capability report are this machine's; the LLM
// provider, model downloads and voices are stand-ins that never touch the
// network.
//
//	wizard -step 3 -state failed -dark -lang ru
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	gioapp "gioui.org/app"

	"github.com/xlsft/companion/internal/app"
	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	paudio "github.com/xlsft/companion/internal/platform/audio"
	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/shortcut"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui"
	"github.com/xlsft/companion/internal/ui/wizard"
	"github.com/xlsft/companion/pkg/contracts"
)

func main() {
	step := flag.Int("step", 1, "step to show, 1…6")
	state := flag.String("state", "fresh", "fresh (not checked yet) or failed (a typical failure)")
	dark := flag.Bool("dark", true, "dark theme")
	lang := flag.String("lang", "ru", "interface language (ru, en)")
	flag.Parse()
	if *step < 1 || *step > len(readiness.Steps) || *state != "fresh" && *state != "failed" {
		flag.Usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*step, *state == "failed", *dark, *lang, log); err != nil {
		log.Error("wizard spike", "err", err)
		os.Exit(1)
	}
}

func run(step int, failed, dark bool, lang string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	dir, err := os.MkdirTemp("", "wizard-spike")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(ctx, filepath.Join(dir, "spike.db"))
	if err != nil {
		return err
	}
	svc, err := settings.NewService(ctx, store.NewKV(db))
	if err != nil {
		return err
	}
	if err := svc.Update(ctx, func(s *settings.Settings) { s.UI.Language = lang }); err != nil {
		return err
	}

	ch := &checks{res: map[readiness.Step]readiness.Result{}}
	gate, err := readiness.Open(ctx, &memStore{}, ch.gate())
	if err != nil {
		return err
	}
	target := readiness.Steps[step-1]
	for _, st := range readiness.Steps[:step-1] {
		ch.set(st, readiness.Result{Status: readiness.Passed})
		if _, err := gate.Run(ctx, st); err != nil {
			return fmt.Errorf("pass %s: %w", st, err)
		}
	}
	ch.set(target, readiness.Result{Status: readiness.Failed, Code: "not_wired"})
	if failed {
		ch.set(target, failures[target])
		if _, err := gate.Run(ctx, target); err != nil {
			return fmt.Errorf("fail %s: %w", target, err)
		}
	}

	ch.mu.Lock()
	ch.slow = true
	ch.mu.Unlock()

	prov := &provider{}
	if step > 2 || failed && target == readiness.StepBYOK {
		prov.info = &providers.Info{ID: "prov_1", HasKey: true,
			Input: providers.Input{Kind: llm.KindAnthropic, BaseURL: "https://api.anthropic.com", Model: "claude-sonnet-5-5"}}
	}
	backends := wizard.Backends{
		Permissions: newHotkey(),
		Provider:    prov,
		STT:         &stt{Capturer: audio.NewCapturer(paudio.New()), dir: dir},
		TTS:         &voices{cfg: wizard.TTSConfig{Provider: wizard.TTSElevenLabs, Speed: 1, HasKey: step > 4}},
	}
	diagnose := func(ctx context.Context) (contracts.CapabilityReport, error) { return detect.New().Detect(ctx) }
	shell := ui.New(ui.Options{Mode: ui.ModeSetup, Settings: svc, Gate: gate, Setup: backends,
		Diagnose: diagnose, Quit: stop, Log: log, Dark: dark, Version: "0.1.0-dev"})
	go func() {
		if err := app.New(log, shell).Run(ctx); err != nil {
			log.Error("spike exited with error", "err", err)
		}
		stop()
		db.Close()
		os.RemoveAll(dir)
		os.Exit(0)
	}()
	gioapp.Main()
	return nil
}

// failures are typical failed results per step, as the wired checks would
// report them.
var failures = map[readiness.Step]readiness.Result{
	readiness.StepPermissions: {Status: readiness.Failed, Code: readiness.PermNeedsBinding, Detail: map[string]string{
		string(contracts.CapMicrophone): readiness.PermGranted, string(contracts.CapScreenCapture): readiness.PermGranted,
		string(contracts.CapGlobalHotkey): readiness.PermNeedsBinding, string(contracts.CapOverlayClickThrough): readiness.PermFallback}},
	readiness.StepBYOK: {Status: readiness.Failed, Code: "auth", Detail: providers.TestResult{ProviderID: "prov_1", Steps: []providers.StepResult{
		{ID: providers.StepConnection, Status: providers.StatusOK, MessageKey: "provider.connection.ok"},
		{ID: providers.StepAuthModel, Status: providers.StatusFailed, MessageKey: "provider.error.auth", Code: "auth"},
		{ID: providers.StepCapabilities, Status: providers.StatusSkipped, MessageKey: "provider.skipped"},
	}}.Detail()},
	readiness.StepSTT:      {Status: readiness.Failed, Code: "mismatch"},
	readiness.StepTTS:      {Status: readiness.Failed, Code: "playback_failed"},
	readiness.StepPlatform: {Status: readiness.Failed, Code: "unavailable"},
	readiness.StepEndToEnd: {Status: readiness.Failed, Code: "llm_failed", Detail: map[string]string{
		"speech": "ok", "stt": "ok", "capture": "ok", "llm": "timeout", "tts": "skipped", "pointing": "skipped"}},
}

type memStore struct {
	mu   sync.Mutex
	data []byte
}

func (m *memStore) Load(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data, nil
}

func (m *memStore) Save(_ context.Context, d []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = append([]byte(nil), d...)
	return nil
}

// checks return a settable result per step; nothing is actually tested.
type checks struct {
	mu   sync.Mutex
	res  map[readiness.Step]readiness.Result
	slow bool // pause like a real check, once the window is up
}

func (c *checks) set(s readiness.Step, r readiness.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.res[s] = r
}

func (c *checks) gate() map[readiness.Step]readiness.Check {
	m := map[readiness.Step]readiness.Check{}
	for _, s := range readiness.Steps {
		m[s] = func(ctx context.Context) readiness.Result {
			c.mu.Lock()
			r, slow := c.res[s], c.slow
			c.mu.Unlock()
			if slow {
				// Long enough to see the running state.
				select {
				case <-time.After(700 * time.Millisecond):
				case <-ctx.Done():
					return readiness.Result{Status: readiness.Failed, Code: "canceled"}
				}
			}
			return r
		}
	}
	return m
}

// hotkey keeps the shortcuts in memory and pretends the system took
// them after a short delay; the real binding is the companion's.
type hotkey struct {
	mu sync.Mutex
	st wizard.HotkeyStatus
}

func newHotkey() *hotkey {
	return &hotkey{st: wizard.HotkeyStatus{Talk: shortcut.DefaultTalk, Cancel: shortcut.DefaultCancel}}
}

func (h *hotkey) Hotkeys() wizard.HotkeyStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.st
}

func (h *hotkey) ApplyHotkeys(ctx context.Context, talk, cancel string) wizard.HotkeyStatus {
	select {
	case <-time.After(600 * time.Millisecond):
	case <-ctx.Done():
		return wizard.HotkeyStatus{Talk: talk, Cancel: cancel, State: wizard.HotkeyFailed, Code: "canceled"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.st = wizard.HotkeyStatus{Talk: talk, Cancel: cancel, State: wizard.HotkeyApplied, ToggleOnly: true}
	return h.st
}

// provider keeps the configured endpoint in memory; keys are dropped.
type provider struct {
	mu   sync.Mutex
	info *providers.Info
}

func (p *provider) List(context.Context) ([]*providers.Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.info == nil {
		return nil, nil
	}
	return []*providers.Info{p.info}, nil
}

func (p *provider) save(id string, in providers.Input) (*providers.Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if in.Model == "" {
		return nil, errors.New("model required")
	}
	has := p.info != nil && p.info.HasKey || in.APIKey != ""
	in.APIKey = ""
	p.info = &providers.Info{ID: id, Input: in, HasKey: has}
	return p.info, nil
}

func (p *provider) Create(_ context.Context, in providers.Input) (*providers.Info, error) {
	return p.save("prov_1", in)
}

func (p *provider) Update(_ context.Context, id string, in providers.Input) (*providers.Info, error) {
	return p.save(id, in)
}

// stt uses this machine's hardware and microphones and the built-in model
// catalogue; Install only pretends to download.
type stt struct {
	audio.Capturer
	dir       string
	mu        sync.Mutex
	installed []models.InstalledModel
}

func (s *stt) Hardware(ctx context.Context) hw.Profile { return hw.Detect(ctx, s.dir) }
func (s *stt) Catalog() (models.Catalog, error)        { return models.BuiltinCatalog() }

func (s *stt) Installed() ([]models.InstalledModel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]models.InstalledModel(nil), s.installed...), nil
}

func (s *stt) Install(ctx context.Context, m models.Manifest, progress func(models.Progress)) error {
	const steps = 40
	for i := 1; i <= steps; i++ {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		done := m.Size * int64(i) / steps
		progress(models.Progress{Done: done, Total: m.Size, Rate: float64(m.Size) / 4, ETA: time.Duration(steps-i) * 100 * time.Millisecond})
	}
	s.mu.Lock()
	s.installed = append(s.installed, models.InstalledModel{ID: m.ID, Size: m.Size})
	s.mu.Unlock()
	return nil
}

func (s *stt) SelectDevice(context.Context, string) error { return nil }
func (s *stt) LastTranscript() string {
	return "привет это проверка микрофона"
}

type voices struct {
	mu  sync.Mutex
	cfg wizard.TTSConfig
}

func (v *voices) Config(context.Context) (wizard.TTSConfig, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cfg, nil
}

func (v *voices) Save(_ context.Context, cfg wizard.TTSConfig, key string) (wizard.TTSConfig, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	cfg.HasKey = v.cfg.HasKey || key != ""
	v.cfg = cfg
	return cfg, nil
}

func (v *voices) Voices(context.Context) ([]tts.Voice, error) {
	return []tts.Voice{
		{ID: "alice", Name: "Алиса", Languages: []string{"ru"}},
		{ID: "george", Name: "George", Languages: []string{"en"}},
		{ID: "nova", Name: "Nova", Languages: []string{"en", "ru"}},
	}, nil
}
