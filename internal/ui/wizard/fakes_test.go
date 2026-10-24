package wizard

import (
	"context"
	"errors"
	"image"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

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

// checks returns configurable results per step.
type checks struct {
	mu  sync.Mutex
	res map[readiness.Step]readiness.Result
	// block, if set for a step, makes its check wait for ctx.
	block map[readiness.Step]bool
	runs  map[readiness.Step]int
}

func newChecks() *checks {
	c := &checks{res: map[readiness.Step]readiness.Result{}, block: map[readiness.Step]bool{}, runs: map[readiness.Step]int{}}
	for _, s := range readiness.Steps {
		c.res[s] = readiness.Result{Status: readiness.Failed, Code: "not_wired"}
	}
	return c
}

func (c *checks) set(s readiness.Step, r readiness.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.res[s] = r
}

func (c *checks) passAll() {
	for _, s := range readiness.Steps {
		c.set(s, readiness.Result{Status: readiness.Passed})
	}
}

func (c *checks) count(s readiness.Step) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs[s]
}

func (c *checks) gateChecks() map[readiness.Step]readiness.Check {
	m := map[readiness.Step]readiness.Check{}
	for _, s := range readiness.Steps {
		m[s] = func(ctx context.Context) readiness.Result {
			c.mu.Lock()
			c.runs[s]++
			r, block := c.res[s], c.block[s]
			c.mu.Unlock()
			if block {
				<-ctx.Done()
				return readiness.Result{Status: readiness.Failed, Code: "canceled"}
			}
			return r
		}
	}
	return m
}

// fakePerm stores the chords and answers applies with result.
type fakePerm struct {
	mu      sync.Mutex
	st      HotkeyStatus
	result  HotkeyStatus // State and Code of the next apply
	applies int
}

func newFakePerm() *fakePerm {
	return &fakePerm{st: HotkeyStatus{Talk: "super+space", Cancel: "super+backspace"}, result: HotkeyStatus{State: HotkeyApplied}}
}

func (f *fakePerm) Hotkeys() HotkeyStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakePerm) ApplyHotkeys(_ context.Context, talk, cancel string) HotkeyStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies++
	st := f.result
	st.Talk, st.Cancel = talk, cancel
	if st.State != HotkeyFailed {
		f.st = st
	}
	return st
}

type fakeProvider struct {
	mu   sync.Mutex
	info *providers.Info
	keys []string
}

func (f *fakeProvider) List(context.Context) ([]*providers.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.info == nil {
		return nil, nil
	}
	return []*providers.Info{f.info}, nil
}

func (f *fakeProvider) save(id string, in providers.Input) (*providers.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.Model == "" {
		return nil, errors.New("invalid")
	}
	has := f.info != nil && f.info.HasKey
	if in.APIKey != "" {
		f.keys = append(f.keys, in.APIKey)
		has = true
	}
	in.APIKey = ""
	f.info = &providers.Info{ID: id, Input: in, HasKey: has}
	return f.info, nil
}

func (f *fakeProvider) Create(_ context.Context, in providers.Input) (*providers.Info, error) {
	return f.save("prov_1", in)
}

func (f *fakeProvider) Update(_ context.Context, id string, in providers.Input) (*providers.Info, error) {
	return f.save(id, in)
}

func (f *fakeProvider) savedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

type fakeSTT struct {
	audio.Capturer
	mu        sync.Mutex
	installed []models.InstalledModel
	// installBlocks makes Install wait for cancellation.
	installBlocks bool
	canceled      atomic.Bool
	device        string
}

func newFakeSTT() *fakeSTT {
	return &fakeSTT{Capturer: audio.NewCapturer(&audio.FakeSource{Realtime: true, DeviceList: []audio.Device{
		{ID: "mic1", Name: "USB microphone", Default: true}, {ID: "mic2", Name: "Webcam"},
	}})}
}

func (f *fakeSTT) Hardware(context.Context) hw.Profile {
	return hw.Profile{OS: "linux", CPU: hw.CPU{Model: "Test CPU", Arch: "amd64", Cores: 8, AVX2: true, FastSIMD: true},
		Memory: hw.Memory{TotalBytes: 16 << 30, AvailableBytes: 8 << 30}, GPU: hw.GPUInfo{Known: true}}
}

func (f *fakeSTT) Catalog() (models.Catalog, error) { return models.BuiltinCatalog() }

func (f *fakeSTT) Installed() ([]models.InstalledModel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.InstalledModel(nil), f.installed...), nil
}

func (f *fakeSTT) Install(ctx context.Context, m models.Manifest, progress func(models.Progress)) error {
	progress(models.Progress{Done: m.Size / 2, Total: m.Size, Rate: 4 << 20, ETA: 9 * time.Second})
	f.mu.Lock()
	block := f.installBlocks
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		f.canceled.Store(true)
		return ctx.Err()
	}
	f.mu.Lock()
	f.installed = append(f.installed, models.InstalledModel{ID: m.ID, Size: m.Size})
	f.mu.Unlock()
	return nil
}

func (f *fakeSTT) SelectDevice(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.device = id
	return nil
}

func (f *fakeSTT) LastTranscript() string {
	return "привет это проверка микрофона"
}

type fakeTTS struct {
	mu   sync.Mutex
	cfg  TTSConfig
	keys []string
}

func (f *fakeTTS) Config(context.Context) (TTSConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg, nil
}

func (f *fakeTTS) Save(_ context.Context, cfg TTSConfig, key string) (TTSConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg.HasKey = f.cfg.HasKey
	if key != "" {
		f.keys = append(f.keys, key)
		cfg.HasKey = true
	}
	f.cfg = cfg
	return cfg, nil
}

func (f *fakeTTS) Voices(context.Context) ([]tts.Voice, error) {
	return []tts.Voice{{ID: "alloy", Name: "Alloy"}, {ID: "nova", Name: "Nova", Languages: []string{"en"}}}, nil
}

func capReport() contracts.CapabilityReport {
	return contracts.CapabilityReport{
		OS: "linux", Session: contracts.SessionWayland, Desktop: "niri",
		Capabilities: map[contracts.Capability]contracts.CapabilityState{
			contracts.CapMicrophone:          {Status: contracts.StatusAvailable, Backend: "pipewire"},
			contracts.CapGlobalHotkey:        {Status: contracts.StatusUnavailable, Reason: "no GlobalShortcuts portal"},
			contracts.CapScreenCapture:       {Status: contracts.StatusAvailable, Backend: "portal"},
			contracts.CapOverlayClickThrough: {Status: contracts.StatusDegraded, Reason: "no layer-shell"},
		},
	}
}

type harness struct {
	t        *testing.T
	gate     *readiness.Gate
	checks   *checks
	w        *Wizard
	r        *input.Router
	th       *theme.Theme
	lang     i18n.Lang
	size     image.Point
	ready    atomic.Int32
	diag     atomic.Int32
	quit     atomic.Int32
	provider *fakeProvider
	stt      *fakeSTT
	tts      *fakeTTS
	perm     *fakePerm
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, checks: newChecks(), r: new(input.Router), th: theme.New(false, 1), lang: i18n.EN,
		size: image.Pt(1100, 4000), provider: &fakeProvider{}, stt: newFakeSTT(), tts: &fakeTTS{}, perm: newFakePerm()}
	g, err := readiness.Open(context.Background(), &memStore{}, h.checks.gateChecks())
	if err != nil {
		t.Fatal(err)
	}
	h.gate = g
	h.w = New(Config{
		Gate: g,
		Backends: Backends{
			Permissions: h.perm,
			Provider:    h.provider,
			STT:         h.stt,
			TTS:         h.tts,
		},
		Diagnose:      func(context.Context) (contracts.CapabilityReport, error) { return capReport(), nil },
		OnReady:       func() { h.ready.Add(1) },
		OnDiagnostics: func() { h.diag.Add(1) },
		OnQuit:        func() { h.quit.Add(1) },
	})
	t.Cleanup(h.w.Close)
	return h
}

// frame lays out one frame like a window would.
func (h *harness) frame() *op.Ops {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops:         ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Source:      h.r.Source(),
		Constraints: layout.Exact(h.size),
	}
	gtx.Constraints.Min = h.size
	kit.Fill(gtx, h.th.Pal.Bg) // the shell's window paints the background
	h.w.Layout(gtx, h.th, h.lang)
	h.r.Frame(ops)
	return ops
}

// eventually renders frames until cond holds.
func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.frame()
		if cond() {
			// The state may have changed after the frame started; draw it.
			h.frame()
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// click queues a click on an action's button and renders it.
func (h *harness) click(a Action) {
	h.t.Helper()
	b, ok := h.w.btns[a]
	if !ok {
		h.t.Fatalf("no %s button rendered", a)
	}
	b.Click()
	h.frame()
}

func (h *harness) view() readiness.Step { return h.w.snap().view() }

func (h *harness) idle() bool { return !h.w.snap().running }

func (h *harness) has(a Action) bool {
	for _, x := range h.w.frame.actions {
		if x == a {
			return true
		}
	}
	return false
}

func (h *harness) shows(sub string) bool {
	for _, s := range h.w.frame.texts {
		if contains(s, sub) {
			return true
		}
	}
	return false
}

// pass runs the current step's check with a passing backend.
func (h *harness) pass(st readiness.Step) {
	h.t.Helper()
	h.checks.set(st, readiness.Result{Status: readiness.Passed})
	if _, err := h.gate.Run(context.Background(), st); err != nil {
		h.t.Fatal(err)
	}
}
