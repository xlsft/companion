// Package walkthrough guides the user through a multi-step task on their
// screen (OC-68): the model plans a goal and ordered steps, each step is
// shown with overlay hints and spoken, and the walkthrough moves on when
// the model decides so, from what the user says or from what it sees on
// the screen once the user acted (auto-advance), or from the chat card.
// Progress is stored, so an unfinished walkthrough is still there after a
// restart.
//
// The package holds no UI and no model code: the Presenter (internal/talk)
// draws and speaks a step, the Store keeps the progress. Step text is
// model output about the user's screen and is never logged.
package walkthrough

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// CmdGoto jumps to a step; it comes with the step's number, so it is
	// applied with GoTo, not Apply.
	CmdGoto Command = "goto"
)

// Commands lists every command.
var Commands = []Command{CmdNext, CmdPrev, CmdRepeat, CmdPause, CmdResume, CmdStop, CmdGoto}

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
	// Done marks the steps the user passed with next; nil before the
	// first. A step jumped over is not done.
	Done []bool `json:"done,omitempty"`
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
		done := make([]bool, len(s.Plan.Steps))
		copy(done, s.Done)
		done[s.Index] = true
		s.Done = done
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
	return s.presented()
}

// GoTo jumps to step i (0-based), resuming a paused walkthrough.
func (s State) GoTo(i int) (State, Effect, error) {
	if !s.Status.Active() {
		return s, EffectNone, ErrNotActive
	}
	if i < 0 || i >= len(s.Plan.Steps) {
		return s, EffectNone, fmt.Errorf("%w: no step %d", ErrUnknown, i+1)
	}
	s.Index = i
	return s.presented()
}

func (s State) presented() (State, Effect, error) {
	s.Status = StatusRunning
	// The screen has moved on since the plan, or since the last showing.
	s.Fresh = false
	return s, EffectPresent, nil
}

// IsDone reports whether step i was passed.
func (s State) IsDone(i int) bool { return i >= 0 && i < len(s.Done) && s.Done[i] }

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
	// Watching: the screen is watched for the user doing the step, which
	// then moves the walkthrough on by itself.
	Watching bool
	// Steps are every step's title and Done which of them were passed,
	// for the model's picture of the walkthrough.
	Steps []string
	Done  []bool
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
	for i, st := range s.Plan.Steps {
		v.Steps = append(v.Steps, st.Title)
		v.Done = append(v.Done, s.IsDone(i))
	}
	return v
}

// Brief describes the walkthrough for the model: the goal, every step
// with what is done and which is current, and the current instruction.
func (v View) Brief() string {
	var b strings.Builder
	state := "running"
	if v.Status == StatusPaused {
		state = "paused"
	}
	fmt.Fprintf(&b, "Walkthrough in progress (%s). Goal: %q. Steps:", state, v.Goal)
	for i, t := range v.Steps {
		mark := ""
		switch {
		case i == v.Index:
			mark = " [current]"
		case i < len(v.Done) && v.Done[i]:
			mark = " [done]"
		}
		fmt.Fprintf(&b, " %d. %q%s;", i+1, t, mark)
	}
	fmt.Fprintf(&b, " Current step %d of %d: %q, instruction: %q.", v.Index+1, v.Total, v.Title, v.Text)
	return b.String()
}
