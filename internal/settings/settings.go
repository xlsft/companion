// Package settings holds the versioned user settings (PRD FR-UI-004):
// defaults, validation, persistence in the store and live change fan-out.
package settings

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/xlsft/companion/internal/shortcut"
	"github.com/xlsft/companion/pkg/contracts"
)

// CurrentVersion is bumped together with a new entry in upgrades.
const CurrentVersion = 1

type HotkeyMode string

const (
	HotkeyHold      HotkeyMode = "hold"
	HotkeyToggle    HotkeyMode = "toggle"
	HotkeyHandsfree HotkeyMode = "handsfree"
)

type CaptureMoment string

const (
	CaptureStart CaptureMoment = "start"
	CaptureEnd   CaptureMoment = "end"
)

type ScreenTarget string

const (
	ScreenActive ScreenTarget = "active"
	ScreenAll    ScreenTarget = "all"
)

type Voice struct {
	HotkeyMode    HotkeyMode    `json:"hotkey_mode"`
	CaptureMoment CaptureMoment `json:"capture_moment"`
	TTSSpeed      float64       `json:"tts_speed"` // 0.5–1.5
	Language      string        `json:"language"`  // "auto" or a language code
	// Microphone is the capture device ID chosen in setup; "" is the
	// system default.
	Microphone string `json:"microphone,omitempty"`
	// TalkHotkey and CancelHotkey are portable chords ("super+space",
	// internal/shortcut); each platform binds them its own way.
	TalkHotkey   string `json:"talk_hotkey"`
	CancelHotkey string `json:"cancel_hotkey"`
	// DictateHotkey types what is said into the focused field of any app,
	// verbatim (OC-78); "" turns dictation's shortcut off.
	DictateHotkey string `json:"dictate_hotkey"`
	// DictationClipboard lets dictation paste through the clipboard where
	// typing does not work; the clipboard is restored afterwards.
	DictationClipboard bool `json:"dictation_clipboard"`
}

// Models holds provider profile ids per role; empty means not configured yet.
type Models struct {
	Talk             string `json:"talk"`
	Vision           string `json:"vision"`
	Agent            string `json:"agent"`
	DictationCleanup string `json:"dictation_cleanup"`
	Fast             string `json:"fast"`
	Deep             string `json:"deep"`
}

type TTS struct {
	ProviderID string `json:"provider_id"`
	Voice      string `json:"voice"`
	// BaseURL overrides the provider's endpoint, e.g. for a compatible
	// local speech server; "" is the provider's default.
	BaseURL string `json:"base_url,omitempty"`
	// Model is the speech model on that endpoint; "" is the provider's
	// default.
	Model string `json:"model,omitempty"`
	// APIKeyRef points into the OS secret store; never exported (FR-DATA-005).
	APIKeyRef contracts.SecretRef `json:"api_key_ref,omitempty"`
}

type Screen struct {
	Target          ScreenTarget `json:"target"`
	KeepScreenshots bool         `json:"keep_screenshots"`
}

type Memory struct {
	Enabled bool `json:"enabled"`
}

type Privacy struct {
	SaveTranscripts  bool `json:"save_transcripts"`
	ShareDiagnostics bool `json:"share_diagnostics"`
}

type Platform struct {
	LaunchAtLogin bool `json:"launch_at_login"`
	TrayIcon      bool `json:"tray_icon"`
}

// Updates controls the in-app updater (OC-75).
type Updates struct {
	// AutoCheck asks the release feed at start and every few hours.
	AutoCheck bool `json:"auto_check"`
	// Prereleases offers pre-release versions too.
	Prereleases bool `json:"prereleases"`
}

type UI struct {
	Language string `json:"language"` // one of UILanguages
}

// UILanguages are the interface languages, English first as the default.
// The UI's catalogue (internal/ui/i18n) has a translation for each.
var UILanguages = []string{"en", "es", "pt", "fr", "de", "it", "pl", "tr", "uk", "ru", "zh", "ja", "ko"}

// Settings is the whole persisted configuration. Only plain values (no
// slices or maps) so a struct copy is a deep copy.
type Settings struct {
	Version  int      `json:"version"`
	Voice    Voice    `json:"voice"`
	Models   Models   `json:"models"`
	TTS      TTS      `json:"tts"`
	Screen   Screen   `json:"screen"`
	Memory   Memory   `json:"memory"`
	Privacy  Privacy  `json:"privacy"`
	Platform Platform `json:"platform"`
	UI       UI       `json:"ui"`
	Updates  Updates  `json:"updates"`
}

// Defaults favour privacy: nothing is kept unless the user opts in.
func Defaults() Settings {
	return Settings{
		Version: CurrentVersion,
		Voice: Voice{HotkeyMode: HotkeyHold, CaptureMoment: CaptureStart, TTSSpeed: 1.0, Language: "auto",
			TalkHotkey: shortcut.DefaultTalk, CancelHotkey: shortcut.DefaultCancel, DictateHotkey: shortcut.DefaultDictate},
		Screen:   Screen{Target: ScreenActive},
		Memory:   Memory{Enabled: true},
		Platform: Platform{TrayIcon: true},
		UI:       UI{Language: "en"},
		Updates:  Updates{AutoCheck: true},
	}
}

var langCode = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// fitDictate gives settings stored before the dictation shortcut existed
// a Dictate chord that does not clash with the user's own Talk or Cancel.
func (s *Settings) fitDictate() {
	t, c, err := shortcut.ValidatePair(s.Voice.TalkHotkey, s.Voice.CancelHotkey)
	if err != nil || s.Voice.DictateHotkey == "" {
		return
	}
	if _, err := shortcut.ValidateDictate(s.Voice.DictateHotkey, t, c); err != nil {
		s.Voice.DictateHotkey = shortcut.PickDictate(t, c)
	}
}

// Validate reports the first invalid field.
func (s Settings) Validate() error {
	switch s.Voice.HotkeyMode {
	case HotkeyHold, HotkeyToggle, HotkeyHandsfree:
	default:
		return fmt.Errorf("settings: voice.hotkey_mode %q invalid", s.Voice.HotkeyMode)
	}
	switch s.Voice.CaptureMoment {
	case CaptureStart, CaptureEnd:
	default:
		return fmt.Errorf("settings: voice.capture_moment %q invalid", s.Voice.CaptureMoment)
	}
	if !(s.Voice.TTSSpeed >= 0.5 && s.Voice.TTSSpeed <= 1.5) { // also rejects NaN
		return fmt.Errorf("settings: voice.tts_speed %v out of range 0.5–1.5", s.Voice.TTSSpeed)
	}
	if s.Voice.Language != "auto" && !langCode.MatchString(s.Voice.Language) {
		return fmt.Errorf("settings: voice.language %q invalid", s.Voice.Language)
	}
	talk, cancel, err := shortcut.ValidatePair(s.Voice.TalkHotkey, s.Voice.CancelHotkey)
	if err != nil {
		return fmt.Errorf("settings: voice hotkeys: %w", err)
	}
	if s.Voice.DictateHotkey != "" {
		if _, err := shortcut.ValidateDictate(s.Voice.DictateHotkey, talk, cancel); err != nil {
			return fmt.Errorf("settings: voice dictate hotkey: %w", err)
		}
	}
	switch s.Screen.Target {
	case ScreenActive, ScreenAll:
	default:
		return fmt.Errorf("settings: screen.target %q invalid", s.Screen.Target)
	}
	if !slices.Contains(UILanguages, s.UI.Language) {
		return fmt.Errorf("settings: ui.language %q invalid", s.UI.Language)
	}
	return nil
}
