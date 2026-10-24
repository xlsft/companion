// Package walkthrough guides the user through a multi-step task on their
// screen (OC-68): the model plans a goal and ordered steps, each step is
// shown with overlay hints and spoken, and the user moves through them by
// voice or from the chat card. Progress is stored, so an unfinished
// walkthrough is still there after a restart.
//
// The package holds no UI and no model code: the Presenter (internal/talk)
// draws and speaks a step, the Store keeps the progress. Step text is
// model output about the user's screen and is never logged.
package walkthrough

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Status is where a walkthrough is.
type Status string

const (
	StatusIdle    Status = "idle" // none started
	StatusRunning Status = "running"
	StatusPaused  Status = "paused"
	StatusDone    Status = "done"    // the last step was passed
	StatusStopped Status = "stopped" // ended by the user
)

// Active reports whether a walkthrough is in progress, i.e. takes commands.
func (s Status) Active() bool { return s == StatusRunning || s == StatusPaused }

// Command is a user's instruction to the walkthrough.
type Command string

const (
	CmdNext   Command = "next"
	CmdPrev   Command = "previous"
	CmdRepeat Command = "repeat"
	CmdPause  Command = "pause"
	CmdResume Command = "resume"
	CmdStop   Command = "stop"
)

// Commands lists every command.
var Commands = []Command{CmdNext, CmdPrev, CmdRepeat, CmdPause, CmdResume, CmdStop}

// Step is one step of a plan.
type Step struct {
	Title string `json:"title"`
	// Say is what is spoken for the step.
	Say string `json:"say"`
	// Shapes are annotate shapes (overlay.ShapeSchema) normalized to the
	// screenshot the step was planned on; empty for none.
	Shapes json.RawMessage `json:"shapes,omitempty"`
}

// Plan is the model's walkthrough.
type Plan struct {
	Goal  string `json:"goal"`
	Steps []Step `json:"steps"`
}

// Limits of a plan.
const (
	MaxSteps    = 12
	maxGoalLen  = 120
	maxTitleLen = 80
	maxSayLen   = 400
)

var ErrInvalidPlan = errors.New("invalid walkthrough plan")

// Validate checks the plan's text fields; Shapes are checked by
// FromToolCall, which knows the overlay rules.
func (p Plan) Validate() error {
	if p.Goal == "" || len([]rune(p.Goal)) > maxGoalLen {
		return fmt.Errorf("%w: goal must be 1..%d characters", ErrInvalidPlan, maxGoalLen)
	}
	if len(p.Steps) == 0 || len(p.Steps) > MaxSteps {
		return fmt.Errorf("%w: need 1..%d steps", ErrInvalidPlan, MaxSteps)
	}
	for i, s := range p.Steps {
		if s.Title == "" || len([]rune(s.Title)) > maxTitleLen {
			return fmt.Errorf("%w: step %d title must be 1..%d characters", ErrInvalidPlan, i, maxTitleLen)
		}
		if s.Say == "" || len([]rune(s.Say)) > maxSayLen {
			return fmt.Errorf("%w: step %d say must be 1..%d characters", ErrInvalidPlan, i, maxSayLen)
		}
	}
	return nil
}

// State is a walkthrough's progress; it is what the Store keeps.
type State struct {
	ID     string `json:"id"`
	Plan   Plan   `json:"plan"`
	Status Status `json:"status"`
	// Index is the current step.
	Index int `json:"index"`
	// Fresh: the current step's Shapes were planned on the screenshot
	// that is still on record with the overlay, so they can be drawn as
	// they are. Any other presentation takes a new screenshot first.
	Fresh   bool      `json:"-"`
	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`
}

// Effect is what a transition asks the runner to do.
type Effect int

const (
	EffectNone Effect = iota
	// EffectPresent shows and speaks the current step.
	EffectPresent
	// EffectHush stops speaking; the hints stay.
	EffectHush
	// EffectEnd stops speaking and removes the hints.
	EffectEnd
)

var (
	// ErrNotActive: there is no running or paused walkthrough.
	ErrNotActive = errors.New("no walkthrough in progress")
	ErrUnknown   = errors.New("unknown walkthrough command")
)

// Apply is the state machine: idle → running(i) ↔ paused → done | stopped.
// Moving while paused resumes. Previous on the first step and Next on the
// last one are not errors: the first repeats, the last finishes.
func (s State) Apply(cmd Command) (State, Effect, error) {
	if !s.Status.Active() {
		return s, EffectNone, ErrNotActive
	}
	last := len(s.Plan.Steps) - 1
	switch cmd {
	case CmdNext:
		if s.Index >= last {
			s.Status = StatusDone
			return s, EffectEnd, nil
		}
		s.Index++
	case CmdPrev:
		s.Index = max(s.Index-1, 0)
	case CmdRepeat:
	case CmdPause:
		if s.Status == StatusPaused {
			return s, EffectNone, nil
		}
		s.Status = StatusPaused
		return s, EffectHush, nil
	case CmdResume:
		if s.Status == StatusRunning {
			return s, EffectNone, nil
		}
	case CmdStop:
		s.Status = StatusStopped
		return s, EffectEnd, nil
	default:
		return s, EffectNone, fmt.Errorf("%w %q", ErrUnknown, cmd)
	}
	s.Status = StatusRunning
	// The screen has moved on since the plan, or since the last showing.
	s.Fresh = false
	return s, EffectPresent, nil
}

// View is a walkthrough as the chat card shows it. Its text must never be
// logged.
type View struct {
	Status Status
	Goal   string
	// Index is the current step, Total the number of steps.
	Index, Total int
	Title, Text  string
	// Prev is the title of the step before the current one, shown as done.
	Prev string
	// Preparing: the current step's hints are being worked out on a new
	// screenshot.
	Preparing bool
}

// Active reports whether the card is shown.
func (v View) Active() bool { return v.Status.Active() }

func (s State) view() View {
	v := View{Status: s.Status, Goal: s.Plan.Goal, Index: s.Index, Total: len(s.Plan.Steps)}
	if s.Index >= 0 && s.Index < len(s.Plan.Steps) {
		v.Title, v.Text = s.Plan.Steps[s.Index].Title, s.Plan.Steps[s.Index].Say
	}
	if s.Index > 0 && s.Index <= len(s.Plan.Steps) {
		v.Prev = s.Plan.Steps[s.Index-1].Title
	}
	return v
}
