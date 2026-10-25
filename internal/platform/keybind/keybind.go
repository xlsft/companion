// Package keybind makes the Talk, Cancel and Dictate shortcuts work on desktops
// where an app cannot take global keys itself: it binds them to
// "companion ctl ..." in the compositor's own configuration and reloads it.
//
//	X11, GlobalShortcuts portal, Windows, macOS -> MethodService: the hotkey
//	                                                service binds the chord
//	Hyprland                                    -> hyprland.conf block (its
//	                                                portal never binds keys)
//	niri                                        -> config.kdl block, validated
//	Sway                                        -> config.d drop-in or block
//	GNOME without the portal                    -> custom keybindings (gsettings)
//	anything else (KDE without the portal, ...) -> MethodManual: Unsupported
//
// Config files are edited only inside a marked block ("companion" between
// ">>>" and "<<<" comment lines) that is replaced on every apply, after a
// one-time backup of the original next to it (".companion.bak").
package keybind

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/xlsft/companion/internal/doubletap"
	"github.com/xlsft/companion/internal/platform/keys"
	"github.com/xlsft/companion/internal/shortcut"
	"github.com/xlsft/companion/pkg/contracts"
)

// Method is how a desktop gets the shortcuts.
type Method string

const (
	MethodService  Method = "service"
	MethodNiri     Method = "niri"
	MethodSway     Method = "sway"
	MethodHyprland Method = "hyprland"
	MethodGNOME    Method = "gnome"
	MethodManual   Method = "manual"
)

// Resolve picks the method. primary reports that the hotkey service holds
// a backend binding chords itself (key grab, portal, OS API).
func Resolve(compositor, desktop string, primary bool) Method {
	switch {
	case compositor == "hyprland":
		return MethodHyprland
	case primary:
		return MethodService
	case compositor == "niri":
		return MethodNiri
	case compositor == "sway":
		return MethodSway
	case strings.Contains(strings.ToLower(desktop), "gnome"):
		return MethodGNOME
	}
	return MethodManual
}

// ToggleOnly reports whether the method's binds fire on press only, so
// Talk is press-to-start, press-to-stop.
func (m Method) ToggleOnly() bool {
	switch m {
	case MethodSway, MethodHyprland, MethodService:
		return false
	}
	return true
}

// TapBinds reports whether the method can bind a lone modifier on
// release: each clean tap then runs "ctl tap-<key>" and the core counts
// two of them as a double tap. niri binds need a non-modifier key, and
// GNOME's custom shortcuts cannot be a lone modifier either.
func (m Method) TapBinds() bool { return m == MethodSway || m == MethodHyprland }

// ConfigBased reports whether the method writes a desktop configuration.
func (m Method) ConfigBased() bool {
	switch m {
	case MethodNiri, MethodSway, MethodHyprland, MethodGNOME:
		return true
	}
	return false
}

// Request is what to bind.
type Request struct {
	Talk, Cancel contracts.Hotkey
	// Dictate is the dictation chord (OC-78); a zero Hotkey binds none.
	Dictate contracts.Hotkey
	// DoubleTap is the modifier whose double tap toggles Talk
	// (doubletap.Keys, OC-133); "" binds none. Only methods with
	// TapBinds can bind it.
	DoubleTap string
	// Exe is the command the binds run, the absolute path of this binary.
	Exe string
}

// Runner runs a tool and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Env is the machine the methods act on; tests point it at temp dirs and
// fake tools.
type Env struct {
	// ConfigHome is $XDG_CONFIG_HOME, or ~/.config.
	ConfigHome string
	Getenv     func(string) string
	Run        Runner
	// LookPath reports whether a tool is installed.
	LookPath func(string) (string, error)
}

// DefaultEnv is the real user environment.
func DefaultEnv() Env {
	ch := os.Getenv("XDG_CONFIG_HOME")
	if ch == "" {
		if home, err := os.UserHomeDir(); err == nil {
			ch = filepath.Join(home, ".config")
		}
	}
	return Env{ConfigHome: ch, Getenv: os.Getenv, Run: execRun, LookPath: exec.LookPath}
}

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (e Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

func (e Env) has(tool string) bool {
	if e.LookPath == nil {
		return false
	}
	_, err := e.LookPath(tool)
	return err == nil
}

// Error codes. Messages carry codes and technical causes only, never
// config contents.
const (
	CodeConfigMissing = "config_missing"
	CodeReadFailed    = "read_failed"
	CodeWriteFailed   = "write_failed"
	CodeInvalid       = "config_invalid" // the desktop's validator rejected it
	CodeReloadFailed  = "reload_failed"
	CodeConflict      = "hotkey_conflict"
	CodeDamagedBlock  = "damaged_block" // a start marker without its end
	CodeToolFailed    = "tool_failed"
	CodeUnsupported   = "unsupported"
	CodeBadChord      = "bad_chord"
)

// Error is a failed apply.
type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return "keybind: " + e.Code
	}
	return "keybind: " + e.Code + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

func fail(code string, err error) error { return &Error{Code: code, Err: err} }

// ErrorCode is the Error code of err, "" for nil and "tool_failed" for
// anything else.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, contracts.ErrUnsupported) {
		return CodeUnsupported
	}
	return CodeToolFailed
}

// Result is what an apply changed.
type Result struct {
	Method Method
	// Path is the file written, "" for gsettings.
	Path string
	// Changed is false when the desktop already had exactly these binds.
	Changed bool
}

// Apply binds req with method m. MethodService has nothing to write and
// returns at once; MethodManual returns Unsupported.
func Apply(ctx context.Context, m Method, env Env, req Request) (Result, error) {
	if err := req.check(); err != nil {
		return Result{Method: m}, err
	}
	if req.DoubleTap != "" && !m.TapBinds() && m != MethodService {
		return Result{Method: m}, fail(CodeUnsupported, contracts.Unsupported(contracts.CapDoubleTap))
	}
	var r Result
	var err error
	switch m {
	case MethodService:
		return Result{Method: m}, nil
	case MethodNiri:
		r, err = applyNiri(ctx, env, req)
	case MethodSway:
		r, err = applySway(ctx, env, req)
	case MethodHyprland:
		r, err = applyHyprland(ctx, env, req)
	case MethodGNOME:
		r, err = applyGNOME(ctx, env, req)
	default:
		return Result{Method: m}, fail(CodeUnsupported, contracts.Unsupported(contracts.CapGlobalHotkey))
	}
	r.Method = m
	return r, err
}

// Applied reports whether the desktop's configuration already holds
// exactly the binds Apply would write, e.g. from an earlier run.
func Applied(ctx context.Context, m Method, env Env, req Request) bool {
	if req.check() != nil {
		return false
	}
	switch m {
	case MethodNiri:
		return fileHasBlock(niriPath(env), niriBlock(req))
	case MethodSway:
		p, _ := swayTarget(env)
		return fileHasBlock(p, swayBlock(req))
	case MethodHyprland:
		return fileHasBlock(hyprPath(env), hyprBlock(req))
	case MethodGNOME:
		return gnomeApplied(ctx, env, req)
	}
	return false
}

// chords are the chords req binds.
func (r Request) chords() []contracts.Hotkey {
	if r.Dictate.Key == "" {
		return []contracts.Hotkey{r.Talk, r.Cancel}
	}
	return []contracts.Hotkey{r.Talk, r.Cancel, r.Dictate}
}

// takenIn reports whether a chord of r is in used.
func (r Request) takenIn(used map[contracts.Hotkey]bool) bool {
	for _, h := range r.chords() {
		if used[h] {
			return true
		}
	}
	return false
}

func (r Request) check() error {
	for _, h := range r.chords() {
		if _, ok := keys.XKBName(h.Key); !ok {
			return fail(CodeBadChord, fmt.Errorf("key %q", h.Key))
		}
	}
	if r.Exe == "" {
		return fail(CodeBadChord, errors.New("no command"))
	}
	if !doubletap.Valid(r.DoubleTap) {
		return fail(CodeBadChord, errors.New("double-tap key"))
	}
	return nil
}

// Commands are the ctl command lines for binding by hand, the last resort
// where nothing can be set automatically.
func Commands(exe string) (talk, cancel string) {
	e := shellWord(exe)
	return e + " ctl talk-toggle", e + " ctl cancel"
}

// TapCommand is the ctl command line a release bind of key runs.
func TapCommand(exe, key string) string { return shellWord(exe) + " ctl tap-" + key }

// tapKeys are the XKB names of both sides of a double-tap key.
var tapKeys = map[string][2]string{
	"ctrl":  {"Control_L", "Control_R"},
	"shift": {"Shift_L", "Shift_R"},
	"alt":   {"Alt_L", "Alt_R"},
	"super": {"Super_L", "Super_R"},
}

// DictateCommand is the ctl command line of the dictation shortcut.
func DictateCommand(exe string) string { return shellWord(exe) + " ctl dictate-toggle" }

// shellWord quotes s for sh-like exec lines when it has special characters.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fromXKB maps a lower-cased xkb keysym name back to our key name.
var fromXKB = func() map[string]string {
	m := map[string]string{}
	for _, k := range shortcut.Keys() {
		if x, ok := keys.XKBName(k); ok {
			m[strings.ToLower(x)] = k
		}
	}
	for a, k := range map[string]string{"esc": "escape", "enter": "return", "pageup": "pageup", "pagedown": "pagedown", "page_up": "pageup", "page_down": "pagedown"} {
		m[a] = k
	}
	return m
}()

// parseForeign reads a chord written in a desktop's own syntax, with mods
// mapping its modifier names. ok is false for anything that is not a plain
// key chord (mouse buttons, unknown keys or modifiers).
func parseForeign(s string, mods map[string]contracts.Modifier) (contracts.Hotkey, bool) {
	var h contracts.Hotkey
	parts := strings.Split(s, "+")
	for i, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if i < len(parts)-1 {
			m, ok := mods[p]
			if !ok {
				return h, false
			}
			h.Mods |= m
			continue
		}
		k, ok := fromXKB[p]
		if !ok {
			return h, false
		}
		h.Key = k
	}
	return h, h.Key != ""
}
