package main

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
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

type setupPermissions struct{ c *core }

func (p setupPermissions) Hotkeys() wizard.HotkeyStatus { return p.c.hotkeyStatus() }

func (p setupPermissions) ApplyHotkeys(ctx context.Context, talk, cancel string) wizard.HotkeyStatus {
	return p.c.applyHotkeys(ctx, talk, cancel)
}

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
	cfg := wizard.TTSConfig{Provider: s.TTS.ProviderID, Voice: s.TTS.Voice, Speed: s.Voice.TTSSpeed}
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
	case ttsOpenAI, ttsElevenLabs:
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
		s.Voice.TTSSpeed = speed
	})
}

// Voices lists the saved provider's voices with the stored key.
func (t setupTTS) Voices(ctx context.Context) ([]tts.Voice, error) {
	prov, _ := t.c.ttsProvider(ctx, t.c.settings.Get())
	if prov == nil {
		return nil, errNoVoice
	}
	return prov.ListVoices(ctx)
}
