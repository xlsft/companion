package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/audio/playback"
	"github.com/xlsft/companion/internal/chat"
	"github.com/xlsft/companion/internal/dictation"
	"github.com/xlsft/companion/internal/displays"
	"github.com/xlsft/companion/internal/history"
	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/memory"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/overlay"
	paudio "github.com/xlsft/companion/internal/platform/audio"
	"github.com/xlsft/companion/internal/platform/capture"
	pdisplays "github.com/xlsft/companion/internal/platform/displays"
	"github.com/xlsft/companion/internal/platform/hotkeys"
	"github.com/xlsft/companion/internal/platform/insert"
	poverlay "github.com/xlsft/companion/internal/platform/overlay"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/secrets"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/setupcheck"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/stt/protocol"
	"github.com/xlsft/companion/internal/stt/supervisor"
	"github.com/xlsft/companion/internal/talk"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/wizard"
	"github.com/xlsft/companion/internal/walkthrough"
	"github.com/xlsft/companion/pkg/contracts"
)

// core holds every long-lived service of the process and their wiring.
// Platform pieces that are unavailable on this machine stay nil with the
// reason in their *Err field: the UI reports them honestly instead of the
// process refusing to start.
type core struct {
	log      *slog.Logger
	dataDir  string
	db       *store.DB
	settings *settings.Service
	gate     *readiness.Gate
	report   contracts.CapabilityReport
	http     *http.Client

	secrets   contracts.SecretStore
	providers *providers.Service
	history   *history.Repo
	memory    *memory.Service

	modelsDir  string
	catalog    models.Catalog
	activator  *models.Activator
	downloader *models.Downloader
	sidecar    string // path of the stt-sidecar binary

	mic    audio.Capturer
	player playback.Player

	displays    *displays.Tracker
	displaysErr error
	capturer    *capture.Capturer
	captureErr  error
	overlayHost poverlay.Host
	overlayErr  error
	overlay     *overlay.Engine

	stt     *supervisor.Supervisor
	speaker *speakerSwitch

	hotkeys    *hotkeys.Service
	hotkeysErr error
	// ctlSeen: a command arrived through the control socket, so the
	// compositor binding the setup asks for exists (StepPermissions).
	ctlSeen atomic.Bool
	// hkMu serialises registering and applying the shortcuts; hkBound
	// are the chords registered, hkStatus how binding them went.
	hkMu     sync.Mutex
	hkBound  [3]string
	hkStatus wizard.HotkeyStatus
	// dictateErr is why the service refused the Dictate chord while it
	// took Talk and Cancel.
	dictateErr error

	// dictation types what is said into the focused field (OC-78).
	inserter  *insert.Inserter
	dictation *dictation.Service

	sttCheck *setupcheck.STT // keeps the last setup transcript in memory

	talk *session.Manager
	orch *talk.Orchestrator
	// ctl is the input path of hotkeys and chat: it adds the overlay
	// clearing that cancel and barge-in need (OC-65).
	ctl  *talk.Control
	chat *chat.Service
	// guide runs walkthroughs (OC-68); their progress outlives a restart.
	guide *walkthrough.Service

	runCtx context.Context // lives from Start to Stop
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newCore builds the services; sec is the OS keyring in the app and an
// in-memory store in tests.
func newCore(ctx context.Context, log *slog.Logger, dataDir string, db *store.DB, svc *settings.Service, report contracts.CapabilityReport, sec contracts.SecretStore) (*core, error) {
	c := &core{
		log: log, dataDir: dataDir, db: db, settings: svc, report: report,
		// No overall timeout: model and TTS bodies stream for minutes.
		// Each provider bounds its own connect and header phase.
		http: &http.Client{},
	}
	c.secrets = sec
	c.providers = providers.New(store.NewProviderRepo(db), c.secrets, c.http)
	c.history = history.New(db, filepath.Join(dataDir, "history"))
	// Learning follows Settings > Memory; a fact may point at its turn
	// only while transcripts are saved.
	c.memory = memory.New(memory.Config{
		Repo:            memory.NewRepo(db),
		Provider:        memory.ProviderFunc(c.talkProvider),
		Enabled:         func() bool { return c.settings.Get().Memory.Enabled },
		SaveTranscripts: func() bool { return c.settings.Get().Privacy.SaveTranscripts },
		Log:             log,
	})

	var err error
	c.modelsDir = filepath.Join(dataDir, "models")
	if err := os.MkdirAll(c.modelsDir, 0o700); err != nil {
		return nil, err
	}
	if c.catalog, err = models.BuiltinCatalog(); err != nil {
		return nil, fmt.Errorf("model catalog: %w", err)
	}
	c.sidecar = sidecarPath()
	c.activator = &models.Activator{Dir: c.modelsDir, Smoke: models.SidecarSmoke{Path: c.sidecar}}
	c.downloader = &models.Downloader{Client: c.http}

	c.mic = audio.NewCapturer(paudio.New())
	c.player = playback.New(paudio.NewSink())

	if c.displays, c.displaysErr = pdisplays.New(ctx, report); c.displaysErr == nil {
		c.capturer, c.captureErr = capture.New(ctx, report, c.displays)
		if c.overlayHost, c.overlayErr = poverlay.New(ctx, report); c.overlayErr == nil {
			c.overlay = overlay.NewEngine(c.overlayHost, c.displays.Topology)
		}
	} else {
		c.captureErr, c.overlayErr = c.displaysErr, c.displaysErr
	}

	c.stt = supervisor.New(supervisor.Config{Launcher: c.launchSTT, Logger: log})
	c.speaker = &speakerSwitch{}
	c.rebuildSpeaker(ctx, svc.Get())

	c.orch = &talk.Orchestrator{
		Provider: c.talkProvider,
		Speaker:  c.speaker,
		Recorder: &talk.MicRecorder{
			Mic:    c.mic,
			STT:    talk.SupervisedSTT(c.stt),
			Device: func() string { return micDevice(c.settings.Get()) },
		},
		Target: captureRequest(svc.Get()),
		Memory: c.memory,
	}
	if c.capturer != nil {
		c.orch.Capturer = c.capturer
	}
	if c.overlay != nil {
		c.orch.Overlay = c.overlay
	}
	c.talk = session.New(c.orch, session.Config{Settings: sessionSettings(svc.Get())})
	c.inserter = insert.New(report, insert.Options{
		TokenPath:    filepath.Join(dataDir, "remote-desktop.token"),
		UseClipboard: func() bool { return c.settings.Get().Voice.DictationClipboard },
	})
	c.dictation = dictation.New(dictation.Config{
		Recorder: &dictation.MicRecorder{
			Mic: c.mic,
			STT: func(ctx context.Context, opts ...stt.SessionOption) (dictation.STTSession, error) {
				return c.stt.StartSession(ctx, opts...)
			},
			Device:   func() string { return micDevice(c.settings.Get()) },
			Language: func() string { return sttLanguage(c.settings.Get()) },
		},
		Inserter: c.inserter,
		Busy:     c.talkHasMic,
		Log:      log,
	})
	c.ctl = &talk.Control{Session: c.talk}
	presenter := &talk.Presenter{Orch: c.orch}
	if c.overlay != nil {
		c.ctl.Scene = c.overlay
		presenter.Scene = c.overlay
	}
	c.guide = walkthrough.New(walkthrough.Config{Presenter: presenter, Store: walkthrough.NewKVStore(store.NewKV(db)), Log: log})
	c.orch.Guide = c.guide
	c.ctl.Guide = c.guide
	c.chat = chat.New(chat.Config{
		Talk:     c.ctl,
		History:  c.history,
		Context:  c.memory,
		Save:     func() bool { return c.settings.Get().Privacy.SaveTranscripts },
		Caps:     c.caps,
		STTModel: c.sttModelName,
		Log:      log,
	})
	// Pixels reach the chat only when both opt-ins are on: kept screenshots
	// live with a stored message, so there is nothing to attach them to
	// when transcripts are not saved.
	c.orch.Attachments = c.chat
	c.orch.KeepScreenshots = func() bool {
		s := c.settings.Get()
		return s.Screen.KeepScreenshots && s.Privacy.SaveTranscripts
	}
	return c, nil
}

// sidecarPath is the stt-sidecar next to this executable, which is how
// packages and `make build` lay them out.
func sidecarPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "stt-sidecar"
	}
	return filepath.Join(filepath.Dir(exe), "stt-sidecar")
}

func (c *core) Name() string { return "core" }

// Start launches the background parts: the STT sidecar (once a model is
// active), the hotkeys and the settings follower. The Talk hotkey only
// reaches the session once setup is Ready (FR-ONB-002).
func (c *core) Start(ctx context.Context) error {
	ctx, c.cancel = context.WithCancel(ctx)
	c.runCtx = ctx
	if _, err := c.activator.Active(); err == nil {
		c.stt.Start(ctx)
	}
	c.startHotkeys(ctx)
	if err := c.guide.Restore(ctx); err != nil {
		c.log.Warn("walkthrough progress not restored", "err", err)
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.ctl.Run(ctx)
	}()
	// The chat follows the session from here on; without this it showed
	// no requests or replies in the real app.
	if err := c.chat.Start(ctx); err != nil {
		return fmt.Errorf("chat: %w", err)
	}
	ch, unsub := c.settings.Subscribe()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer unsub()
		for {
			select {
			case s, ok := <-ch:
				if !ok {
					return
				}
				c.applySettings(ctx, s)
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

func (c *core) Stop(ctx context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	var errs []error
	// First, so no step speaks or draws while the rest shuts down; the
	// progress stays stored.
	c.guide.Close()
	c.chat.Close()
	c.memory.Close()
	errs = append(errs, c.talk.Close())
	c.dictation.Close()
	errs = append(errs, c.inserter.Close())
	if c.hotkeys != nil {
		errs = append(errs, c.hotkeys.Close())
	}
	errs = append(errs, c.stt.Stop(ctx))
	if c.overlay != nil {
		c.overlay.Clear(ctx)
	}
	if c.overlayHost != nil {
		errs = append(errs, c.overlayHost.Close())
	}
	if c.capturer != nil {
		errs = append(errs, c.capturer.Close())
	}
	if c.displays != nil {
		errs = append(errs, c.displays.Close())
	}
	c.wg.Wait()
	return errors.Join(errs...)
}

// applySettings pushes a settings change to the parts that read it at
// construction; everything else reads settings per request.
func (c *core) applySettings(ctx context.Context, s settings.Settings) {
	if err := c.talk.SetSettings(sessionSettings(s)); err != nil && !errors.Is(err, session.ErrClosed) {
		c.log.Warn("session settings", "err", err)
	}
	c.rebuildSpeaker(ctx, s)
	c.rebindHotkeys(ctx, s)
}

// launchSTT starts the sidecar on the active model; the supervisor calls
// it again after a crash or Reset, so a newly activated model applies.
func (c *core) launchSTT(ctx context.Context) (*stt.Client, error) {
	m, err := c.activator.Active()
	if err != nil {
		return nil, err
	}
	return stt.Start(ctx, stt.Config{
		Path: c.sidecar,
		Init: protocol.InitializeParams{ModelPath: m.Path, Language: sttLanguage(c.settings.Get()), Backend: protocol.BackendCPU},
	})
}

// dictationUI is dictation as the main window shows it.
type dictationUI struct {
	*dictation.Service
	c *core
}

func (d dictationUI) Shortcut() string { return chordLabel(d.c.settings.Get().Voice.DictateHotkey) }

// sttLanguage is the recognition language from settings.
func sttLanguage(s settings.Settings) string {
	if s.Voice.Language == "" {
		return protocol.LanguageAuto
	}
	return s.Voice.Language
}

// talkHasMic reports that a Talk request is recording or decoding, so
// dictation must not open the microphone beside it.
func (c *core) talkHasMic() bool {
	switch c.talk.Current().State {
	case session.StateListening, session.StateTranscribing:
		return true
	}
	return false
}

// restartSTT relaunches recognition after a model change or from
// Degraded. The supervisor lives as long as the core, never as long as
// the request that triggered the restart.
func (c *core) restartSTT() {
	if c.stt.State().Kind == supervisor.Stopped {
		c.stt.Start(c.runCtx)
		return
	}
	c.stt.Reset()
}

// talkProfile is the provider profile answering Talk requests: the one set
// for the role, else the first configured (the one setup edits).
func (c *core) talkProfile(ctx context.Context) (*providers.Info, error) {
	list, err := c.providers.List(ctx)
	if err != nil {
		return nil, err
	}
	want := c.settings.Get().Models.Talk
	for _, p := range list {
		if p.ID == want {
			return p, nil
		}
	}
	if len(list) == 0 {
		return nil, errNoProvider
	}
	return list[0], nil
}

var errNoProvider = errors.New("no language model configured")

func (c *core) talkProvider(ctx context.Context) (llm.Provider, error) {
	p, err := c.talkProfile(ctx)
	if err != nil {
		return nil, err
	}
	prov, _, err := c.providers.Build(ctx, p.ID)
	return prov, err
}

// caps are the chat's live capabilities and header labels.
func (c *core) caps() chat.Capabilities {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cp := chat.Capabilities{}
	if p, err := c.talkProfile(ctx); err == nil {
		cp.Text = true
		cp.Model = p.Input.Model
	}
	if _, err := c.activator.Active(); err == nil {
		cp.Voice = true
	}
	if c.hotkeys != nil && c.hotkeysErr == nil {
		cp.Hotkey = c.talkLabel()
	}
	cp.Speech = c.speaker.label()
	return cp
}

// sttModelName names the active recogniser for voice turns.
func (c *core) sttModelName() string {
	m, err := c.activator.Active()
	if err != nil {
		return ""
	}
	if man, ok := c.catalog.Find(m.ID); ok {
		return man.Name
	}
	return m.ID
}

// micDevice is the microphone chosen in setup; "" is the system default.
func micDevice(s settings.Settings) string { return s.Voice.Microphone }

func captureRequest(s settings.Settings) contracts.CaptureRequest {
	if s.Screen.Target == settings.ScreenAll {
		return contracts.CaptureRequest{Target: contracts.CaptureAllDisplays}
	}
	return contracts.CaptureRequest{Target: contracts.CaptureActiveDisplay}
}

func sessionSettings(s settings.Settings) session.Settings {
	mode := session.ModeHoldToTalk
	switch s.Voice.HotkeyMode {
	case settings.HotkeyToggle:
		mode = session.ModeToggle
	case settings.HotkeyHandsfree:
		mode = session.ModeHandsFree
	}
	order := session.CaptureAtStart
	if s.Voice.CaptureMoment == settings.CaptureEnd {
		order = session.CaptureAtEnd
	}
	return session.Settings{VoiceMode: mode, Capture: order, SpeakReplies: true}
}

// speakerSwitch is the Talk speaker; it is rebuilt when the speech
// settings change, so the next reply uses the new provider or voice.
type speakerSwitch struct {
	mu   sync.Mutex
	cur  *talk.TTSSpeaker
	name string
}

var errNoVoice = errors.New("speech output not configured")

func (s *speakerSwitch) get() *talk.TTSSpeaker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// configured reports whether speech output has a provider and key.
func (s *speakerSwitch) configured() bool { return s.get() != nil }

func (s *speakerSwitch) label() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

func (s *speakerSwitch) Say(ctx context.Context, text string, started func()) error {
	sp := s.get()
	if sp == nil {
		return errNoVoice
	}
	return sp.Say(ctx, text, started)
}

func (s *speakerSwitch) Prefetch(ctx context.Context, text string) {
	if sp := s.get(); sp != nil {
		sp.Prefetch(ctx, text)
	}
}

// ttsSecret is where a speech provider's API key lives in the keyring.
func ttsSecret(provider string) contracts.SecretRef { return secrets.NewRef("tts", provider) }

// rebuildSpeaker builds the speaker from the TTS settings and key; with
// either missing replies are text only.
func (c *core) rebuildSpeaker(ctx context.Context, s settings.Settings) {
	prov, name := c.ttsProvider(ctx, s)
	var sp *talk.TTSSpeaker
	if prov != nil {
		sp = &talk.TTSSpeaker{
			Provider: prov,
			Player:   c.player,
			Voice:    s.TTS.Voice,
			Speed:    func() float64 { return c.settings.Get().Voice.TTSSpeed },
		}
	}
	c.speaker.mu.Lock()
	c.speaker.cur, c.speaker.name = sp, name
	c.speaker.mu.Unlock()
}

// ttsProvider builds the configured speech provider and its header label.
func (c *core) ttsProvider(ctx context.Context, s settings.Settings) (tts.Provider, string) {
	if s.TTS.ProviderID == "" {
		return nil, ""
	}
	key, err := c.secrets.Get(ctx, ttsSecret(s.TTS.ProviderID))
	if err != nil || len(key) == 0 {
		return nil, ""
	}
	cfg := tts.Config{APIKey: string(key), BaseURL: s.TTS.BaseURL}
	label := s.TTS.Voice
	switch s.TTS.ProviderID {
	case ttsElevenLabs:
		return tts.NewElevenLabs(tts.ElevenLabsConfig{Config: cfg, HTTPClient: c.http}), joinLabel("ElevenLabs", label)
	case ttsOpenAI:
		return tts.NewOpenAI(tts.OpenAIConfig{Config: cfg, HTTPClient: c.http}), joinLabel("OpenAI", label)
	}
	return nil, ""
}

const (
	ttsOpenAI     = wizard.TTSOpenAI
	ttsElevenLabs = wizard.TTSElevenLabs
)

func joinLabel(a, b string) string {
	if b == "" {
		return a
	}
	return a + " · " + b
}
