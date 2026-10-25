// Package control is the local control socket of the core: a line protocol
// on $XDG_RUNTIME_DIR/openclick/control.sock that "companion ctl" talks
// to. Its first user is the hotkey fallback for compositors without a
// usable GlobalShortcuts portal (niri, Sway, Hyprland): the user binds a
// compositor key to "companion ctl talk-toggle" and the Server turns the
// command into a contracts.HotkeyEvent.
//
// Protocol: one command per line, one reply line per command.
//
//	ping                              -> ok
//	hotkey <id> press|release|toggle  -> ok | err <code>
//	tap ctrl|shift|alt|super          -> ok | err <code>
//
// "tap" is one clean tap of a lone modifier, sent by a compositor release
// bind; two of them in quick succession toggle the double-tap binding
// (OC-133).
//
// Error codes are fixed technical strings (see the Code* constants), never
// echoes of the input.
package control

import (
	"errors"
	"regexp"
	"strings"

	"github.com/xlsft/companion/internal/doubletap"
	"github.com/xlsft/companion/pkg/contracts"
)

// Binding IDs the ctl CLI addresses. They match what the core registers for
// Talk, Escape and dictation.
const (
	TalkID    contracts.HotkeyID = "talk"
	CancelID  contracts.HotkeyID = "cancel"
	DictateID contracts.HotkeyID = "dictate"
)

// Reply error codes.
const (
	CodeBadCommand     = "bad-command"
	CodeBadID          = "bad-id"
	CodeBadAction      = "bad-action"
	CodeBadKey         = "bad-key"
	CodeUnknownBinding = "unknown-binding"
	CodeBusy           = "busy"
	CodeForbidden      = "forbidden"
	CodeTooLong        = "too-long"
)

// maxLine bounds a command line; real commands are a few dozen bytes.
const maxLine = 256

type Verb int

const (
	VerbPing Verb = iota + 1
	VerbHotkey
	VerbTap
)

type Action string

const (
	ActionPress   Action = "press"
	ActionRelease Action = "release"
	ActionToggle  Action = "toggle"
)

// Command is one parsed protocol line.
type Command struct {
	Verb   Verb
	ID     contracts.HotkeyID
	Action Action
	// Key is the tapped key of VerbTap, a doubletap.Keys entry.
	Key string
}

// ProtocolError carries the reply code for a rejected command.
type ProtocolError struct{ Code string }

func (e *ProtocolError) Error() string { return "control: " + e.Code }

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ParseLine parses one command line (without the newline).
func ParseLine(line string) (Command, error) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return Command{}, &ProtocolError{CodeBadCommand}
	}
	switch f[0] {
	case "ping":
		if len(f) != 1 {
			return Command{}, &ProtocolError{CodeBadCommand}
		}
		return Command{Verb: VerbPing}, nil
	case "hotkey":
		if len(f) != 3 {
			return Command{}, &ProtocolError{CodeBadCommand}
		}
		if !idRE.MatchString(f[1]) {
			return Command{}, &ProtocolError{CodeBadID}
		}
		a := Action(f[2])
		switch a {
		case ActionPress, ActionRelease, ActionToggle:
		default:
			return Command{}, &ProtocolError{CodeBadAction}
		}
		return Command{Verb: VerbHotkey, ID: contracts.HotkeyID(f[1]), Action: a}, nil
	case "tap":
		if len(f) != 2 {
			return Command{}, &ProtocolError{CodeBadCommand}
		}
		if f[1] == "" || !doubletap.Valid(f[1]) {
			return Command{}, &ProtocolError{CodeBadKey}
		}
		return Command{Verb: VerbTap, Key: f[1]}, nil
	}
	return Command{}, &ProtocolError{CodeBadCommand}
}

// Kind maps an action to the event it produces.
func (a Action) Kind() contracts.HotkeyEventKind {
	switch a {
	case ActionRelease:
		return contracts.HotkeyReleased
	case ActionToggle:
		return contracts.HotkeyToggled
	}
	return contracts.HotkeyPressed
}

// ctlCommands are the "companion ctl" subcommands and the lines they send.
// cancel is a press: Escape has no hold semantics.
var ctlCommands = []struct{ name, line string }{
	{"talk-toggle", "hotkey talk toggle"},
	{"talk-press", "hotkey talk press"},
	{"talk-release", "hotkey talk release"},
	{"cancel", "hotkey cancel press"},
	{"dictate-toggle", "hotkey dictate toggle"},
	{"dictate-press", "hotkey dictate press"},
	{"dictate-release", "hotkey dictate release"},
	{"tap-ctrl", "tap ctrl"},
	{"tap-shift", "tap shift"},
	{"tap-alt", "tap alt"},
	{"tap-super", "tap super"},
	{"ping", "ping"},
}

// ErrUnknownCtl is returned by CtlLine for an unknown subcommand.
var ErrUnknownCtl = errors.New("control: unknown ctl command")

// CtlLine returns the protocol line for a ctl subcommand.
func CtlLine(sub string) (string, error) {
	for _, c := range ctlCommands {
		if c.name == sub {
			return c.line, nil
		}
	}
	return "", ErrUnknownCtl
}

// CtlCommands lists the ctl subcommands for usage text.
func CtlCommands() []string {
	out := make([]string, len(ctlCommands))
	for i, c := range ctlCommands {
		out[i] = c.name
	}
	return out
}
