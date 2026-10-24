package wizard

import (
	"context"

	"github.com/xlsft/openclick/internal/audio"
	"github.com/xlsft/openclick/internal/models"
	"github.com/xlsft/openclick/internal/models/hw"
	"github.com/xlsft/openclick/internal/providers"
	"github.com/xlsft/openclick/internal/tts"
)

// Backends are the UI-side operations of the steps: forms, lists, downloads
// and the level meter. The checks themselves are readiness.Check functions
// in the Gate, so the wizard never decides a step's outcome. Signatures use
// only pure-Go domain types, so implementations need not import Gio.
//
// A nil backend leaves its step's controls disabled; the step's check can
// still run (and fails until the app wires it).
type Backends struct {
	Permissions Permissions
	Provider    Provider
	STT         STT
	TTS         TTS
}

// Permissions serves the permissions step. The StepPermissions check
// reports per-capability codes in Result.Detail (readiness.Perm*).
type Permissions interface {
	// HotkeyInstructions is the compositor binding text shown when the
	// check reports global_hotkey = needs_compositor_binding.
	HotkeyInstructions() string
}

// Provider manages the LLM endpoint configured in setup; *providers.Service
// implements it. The wizard edits the first provider of List. The
// StepBYOK check calls providers.Service.Test on that provider and returns
// TestResult.Detail() as Result.Detail.
type Provider interface {
	List(ctx context.Context) ([]*providers.Info, error)
	Create(ctx context.Context, in providers.Input) (*providers.Info, error)
	Update(ctx context.Context, id string, in providers.Input) (*providers.Info, error)
}

// STT serves the speech-recognition step. The StepSTT check records the
// selected microphone, transcribes with the active model and compares with
// readiness.MicPhrase loosely (readiness.PhraseMatches).
type STT interface {
	// Hardware is the detected machine profile (hw.Detect).
	Hardware(ctx context.Context) hw.Profile
	Catalog() (models.Catalog, error)
	Installed() ([]models.InstalledModel, error)
	// Install downloads (unless present), verifies and activates m,
	// reporting download progress. Cancelling ctx aborts the download.
	Install(ctx context.Context, m models.Manifest, progress func(models.Progress)) error
	// Devices and Open are audio.Capturer; the wizard opens a stream only
	// for the level meter and discards its frames.
	Devices(ctx context.Context) ([]audio.Device, error)
	Open(ctx context.Context, deviceID string) (audio.Stream, error)
	// SelectDevice stores the microphone the check (and later Talk) uses;
	// "" is the system default.
	SelectDevice(ctx context.Context, deviceID string) error
	// LastTranscript is the transcript of the last check. It is user
	// content: kept in memory only, never persisted or logged.
	LastTranscript() string
}

// TTS provider identifiers.
const (
	TTSOpenAI     = "openai"     // OpenAI-compatible speech endpoint
	TTSElevenLabs = "elevenlabs" // ElevenLabs
)

// TTSConfig is the speech-output configuration shown in the wizard.
type TTSConfig struct {
	Provider string // TTSOpenAI or TTSElevenLabs
	Voice    string
	Speed    float64 // tts.MinSpeed…tts.MaxSpeed
	// HasKey is read-only: whether a key is stored. The key itself never
	// comes back to the UI.
	HasKey bool
}

// TTS serves the speech-output step. The StepTTS check plays a fixed
// sentence with the saved configuration.
type TTS interface {
	Config(ctx context.Context) (TTSConfig, error)
	// Save stores cfg. apiKey is write-only; "" keeps the stored key.
	Save(ctx context.Context, cfg TTSConfig, apiKey string) (TTSConfig, error)
	// Voices lists the voices of the saved provider.
	Voices(ctx context.Context) ([]tts.Voice, error)
}
