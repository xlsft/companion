package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/wizard"
	"github.com/xlsft/companion/pkg/contracts"
)

// setupBackends are the wizard's step backends over the core's services.
// setupChecks must run first: the STT backend shows its transcript.
func (c *core) setupBackends() wizard.Backends {
	return wizard.Backends{
		Permissions: setupPermissions{c},
		Provider:    c.providers,
		STT:         setupSTT{c},
		TTS:         setupTTS{c},
	}
}

// The model step lists models with the stored key (providers.ModelQuery).
var _ wizard.ProviderModels = (*providers.Service)(nil)

type setupPermissions struct{ c *core }

func (p setupPermissions) Hotkeys() wizard.HotkeyStatus { return p.c.hotkeyStatus() }

func (p setupPermissions) ApplyHotkeys(ctx context.Context, talk, cancel string) wizard.HotkeyStatus {
	return p.c.applyHotkeys(ctx, talk, cancel)
}

func (p setupPermissions) ApplyDictateHotkey(ctx context.Context, chord string) wizard.HotkeyStatus {
	return p.c.applyDictateHotkey(ctx, chord)
}

var _ wizard.DictateHotkeys = setupPermissions{}

type setupSTT struct{ c *core }

func (s setupSTT) Hardware(ctx context.Context) hw.Profile { return hw.Detect(ctx, s.c.modelsDir) }
func (s setupSTT) Catalog() (models.Catalog, error)        { return s.c.catalog, nil }
func (s setupSTT) Installed() ([]models.InstalledModel, error) {
	return models.Installed(s.c.modelsDir)
}

// Install downloads m (resuming a partial file), lets the activator verify
// and smoke-test it, switches to it and restarts recognition on it.
func (s setupSTT) Install(ctx context.Context, m models.Manifest, progress func(models.Progress)) error {
	if _, err := s.c.downloader.Download(ctx, m, s.c.modelsDir, progress); err != nil {
		return err
	}
	if _, err := s.c.activator.Activate(ctx, m.ID); err != nil {
		return err
	}
	s.c.restartSTT()
	return nil
}

var _ wizard.ActiveModeler = setupSTT{}

// ActiveModel is the model recognition runs on, for Settings → Voice.
func (s setupSTT) ActiveModel() string {
	m, err := s.c.activator.Active()
	if err != nil {
		return ""
	}
	return m.ID
}

func (s setupSTT) Devices(ctx context.Context) ([]audio.Device, error) { return s.c.mic.Devices(ctx) }
func (s setupSTT) Open(ctx context.Context, id string) (audio.Stream, error) {
	return s.c.mic.Open(ctx, id)
}

func (s setupSTT) SelectDevice(ctx context.Context, id string) error {
	return s.c.settings.Update(ctx, func(st *settings.Settings) { st.Voice.Microphone = id })
}

// LastEndToEndTranscript is what the full test last heard.
func (s setupSTT) LastEndToEndTranscript() string {
	t, _ := s.c.e2eHeard.Load().(string)
	return t
}

func (s setupSTT) LastTranscript() string {
	if s.c.sttCheck == nil {
		return ""
	}
	return s.c.sttCheck.LastTranscript()
}

type setupTTS struct{ c *core }

var errUnknownTTS = errors.New("unknown speech provider")

func (t setupTTS) Config(ctx context.Context) (wizard.TTSConfig, error) {
	return ttsConfig(ctx, t.c.secrets, t.c.settings.Get()), nil
}

func ttsConfig(ctx context.Context, sec contracts.SecretStore, s settings.Settings) wizard.TTSConfig {
	cfg := wizard.TTSConfig{Provider: s.TTS.ProviderID, BaseURL: s.TTS.BaseURL, Model: s.TTS.Model, Voice: s.TTS.Voice, Speed: s.Voice.TTSSpeed}
	if cfg.Provider != "" {
		key, err := sec.Get(ctx, ttsSecret(cfg.Provider))
		cfg.HasKey = err == nil && len(key) > 0
	}
	return cfg
}

// Save stores the speech settings; the key goes to the keyring only and
// an empty one keeps what is stored for that provider.
func (t setupTTS) Save(ctx context.Context, cfg wizard.TTSConfig, apiKey string) (wizard.TTSConfig, error) {
	if err := saveTTS(ctx, t.c.secrets, t.c.settings, cfg, apiKey); err != nil {
		return wizard.TTSConfig{}, err
	}
	s := t.c.settings.Get()
	// The settings follower rebuilds too, but a new key alone changes no
	// setting; the check right after must use it.
	t.c.rebuildSpeaker(ctx, s)
	return ttsConfig(ctx, t.c.secrets, s), nil
}

func saveTTS(ctx context.Context, sec contracts.SecretStore, svc *settings.Service, cfg wizard.TTSConfig, apiKey string) error {
	switch cfg.Provider {
	case ttsOpenAI, ttsElevenLabs, ttsEdge:
	default:
		return fmt.Errorf("%w: %q", errUnknownTTS, cfg.Provider)
	}
	ref := ttsSecret(cfg.Provider)
	if apiKey != "" {
		if err := sec.Put(ctx, ref, []byte(apiKey)); err != nil {
			return fmt.Errorf("store speech key: %w", err)
		}
	}
	speed := cfg.Speed
	if speed == 0 || math.IsNaN(speed) {
		speed = tts.DefaultSpeed
	}
	speed = math.Min(tts.MaxSpeed, math.Max(tts.MinSpeed, speed))
	return svc.Update(ctx, func(s *settings.Settings) {
		s.TTS.ProviderID, s.TTS.Voice, s.TTS.APIKeyRef = cfg.Provider, cfg.Voice, ref
		s.TTS.BaseURL, s.TTS.Model = strings.TrimSpace(cfg.BaseURL), strings.TrimSpace(cfg.Model)
		s.Voice.TTSSpeed = speed
	})
}

var _ wizard.TTSModels = setupTTS{}

// openAISpeechURL is where an OpenAI speech setup without an address goes.
const openAISpeechURL = "https://api.openai.com/v1"

// ListModels lists the models of the OpenAI-compatible speech server in
// q. Without a typed key the stored one is used, but only for the server
// it was saved with: a key never goes to an address it was not meant for.
func (t setupTTS) ListModels(ctx context.Context, q wizard.TTSModelQuery) ([]string, error) {
	base := cmp.Or(strings.TrimSpace(q.BaseURL), openAISpeechURL)
	key := q.APIKey
	if s := t.c.settings.Get().TTS; key == "" && s.ProviderID == ttsOpenAI && sameServer(cmp.Or(s.BaseURL, openAISpeechURL), base) {
		if k, err := t.c.secrets.Get(ctx, ttsSecret(ttsOpenAI)); err == nil {
			key = string(k)
		}
	}
	return tts.NewOpenAI(tts.OpenAIConfig{Config: tts.Config{BaseURL: base, APIKey: key, Timeout: 15 * time.Second},
		HTTPClient: t.c.http}).ListModels(ctx)
}

// sameServer reports whether two addresses share scheme and host.
func sameServer(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil || ua.Host == "" {
		return false
	}
	ub, err := url.Parse(b)
	return err == nil && strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// Voices lists the saved provider's voices with the stored key.
func (t setupTTS) Voices(ctx context.Context) ([]tts.Voice, error) {
	prov := t.c.voicesProvider(ctx, t.c.settings.Get())
	if prov == nil {
		return nil, errNoVoice
	}
	return prov.ListVoices(ctx)
}
