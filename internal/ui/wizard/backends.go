package wizard

import (
	"context"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/tts"
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

// Permissions serves the permissions step and the shortcut editor in
// Settings. The StepPermissions check reports per-capability codes in
// Result.Detail (readiness.Perm*).
type Permissions interface {
	// Hotkeys is the stored Talk and Cancel chords and how the last apply
	// went.
	Hotkeys() HotkeyStatus
	// ApplyHotkeys validates and stores the chords (internal/shortcut
	// syntax) and makes them work on this system, however it binds keys.
	// It may show a system dialog, so ctx should allow for the user.
	ApplyHotkeys(ctx context.Context, talk, cancel string) HotkeyStatus
}

// HotkeyState is the outcome of applying the shortcuts.
type HotkeyState int

const (
	HotkeyNotApplied HotkeyState = iota
	HotkeyApplied
	HotkeyFailed
	// HotkeyManual: this system cannot be set up automatically; the user
	// binds Commands in its keyboard settings.
	HotkeyManual
)

// HotkeyStatus is what the shortcut editors show.
type HotkeyStatus struct {
	Talk, Cancel string // stored chords
	State        HotkeyState
	// Code is the technical reason of a failure (HotkeyFailed).
	Code string
	// Dictate is the stored dictation chord, "" for none; DictateState
	// and DictateCode are how binding it went (OC-78).
	Dictate      string
	DictateState HotkeyState
	DictateCode  string
	// ToggleOnly: the system's binds fire on press only, so Talk is
	// press to start, press again to send.
	ToggleOnly bool
	// Commands are the commands to bind by hand (HotkeyManual): Talk,
	// Cancel and, with a Dictate chord, Dictate.
	Commands []string
}

// DictateHotkeys is a Permissions backend that also binds the dictation
// shortcut; Settings shows the dictation editor only for one.
type DictateHotkeys interface {
	Hotkeys() HotkeyStatus
	// ApplyDictateHotkey validates, stores and binds the dictation chord
	// beside the stored Talk and Cancel.
	ApplyDictateHotkey(ctx context.Context, chord string) HotkeyStatus
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

// ProviderModels is a Provider backend that also lists an endpoint's
// models; the model step then offers them to pick from. The query carries
// the form as typed and the saved provider's id, so an empty key field can
// use the stored key, which never comes back to the UI. Failures (no
// listing, bad key, offline) leave the model to be typed.
type ProviderModels interface {
	ListModels(ctx context.Context, q providers.ModelQuery) ([]string, error)
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
	// BaseURL and Model point an OpenAI-compatible provider at another
	// server, e.g. a local one; "" keeps the provider's defaults.
	BaseURL, Model string
	Voice          string
	Speed          float64 // tts.MinSpeed…tts.MaxSpeed
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

// TTSModelQuery is the OpenAI-compatible speech server whose models the
// voice step wants, as typed so far.
type TTSModelQuery struct {
	BaseURL string // "" is OpenAI's own API
	// APIKey is the key typed in the form; "" uses the stored key while
	// BaseURL is still the saved server.
	APIKey string
}

// TTSModels is a TTS backend that also lists the models of an
// OpenAI-compatible speech server; the voice step then offers them to pick
// from. Other providers have no model field.
type TTSModels interface {
	ListModels(ctx context.Context, q TTSModelQuery) ([]string, error)
}
