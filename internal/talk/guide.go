package talk

import (
	"context"
	"fmt"
	"math"

	"github.com/xlsft/companion/internal/coordinates"
	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/walkthrough"
	"github.com/xlsft/companion/pkg/contracts"
)

// Guide is the part of *walkthrough.Service the Talk pipeline drives.
type Guide interface {
	View() walkthrough.View
	Start(ctx context.Context, plan walkthrough.Plan, capture contracts.CaptureID) error
	// Control applies the model's walkthrough_control decision, made on
	// step at; walkthrough.ErrMoved when the walkthrough moved meanwhile.
	Control(ctx context.Context, c walkthrough.Control, at int) error
	Revise(ctx context.Context, r walkthrough.Revision) error
	// Hush stops the step's speech; the walkthrough stays where it is.
	Hush()
}

var _ Guide = (*walkthrough.Service)(nil)

// stepToolRounds bounds the model rounds that re-mark or check one step.
const stepToolRounds = 3

// Presenter shows walkthrough steps with the Talk pipeline's overlay,
// screen capture, model and speaker (walkthrough.Presenter).
type Presenter struct {
	Orch *Orchestrator
	// Scene removes a walkthrough's hints; nil without an overlay.
	Scene Scene
}

var _ walkthrough.Presenter = (*Presenter)(nil)

// Show draws the step's hints. A step shown on the plan's own screenshot
// is drawn as planned; any later showing takes a new screenshot and asks
// the model to mark the step on it, since the user has been clicking in
// the meantime. Without an overlay, capture or model the step is only
// said.
func (p *Presenter) Show(ctx context.Context, pr walkthrough.Presentation) (walkthrough.Shown, error) {
	o := p.Orch
	shown := walkthrough.Shown{Step: pr.Step}
	if o.Overlay == nil {
		return shown, nil
	}
	if pr.Capture != "" {
		cmds, err := walkthrough.ShapeCommands(overlay.CallContext{Session: pr.Ref, Capture: pr.Capture, CallID: "step", Segment: -1}, pr.Step.Shapes)
		if err != nil {
			return shown, err
		}
		for _, c := range cmds {
			if err := o.Overlay.Apply(ctx, c); err != nil {
				return shown, err
			}
		}
		if meta, ok := o.captureMeta(pr.Capture); ok {
			shown.Area = hintArea(meta, cmds)
		}
		return shown, nil
	}
	r, err := p.ask(ctx, pr, stepPrompt, stepRequest(pr), false)
	if r.revised {
		shown.Step.Title, shown.Step.Say = r.step.Title, r.step.Say
	}
	shown.Area = r.area
	return shown, err
}

// Check asks the model, on a new screenshot, whether the user has done the
// step; it may move the walkthrough on, mark the step anew or revise it.
func (p *Presenter) Check(ctx context.Context, pr walkthrough.Presentation) (walkthrough.Verdict, error) {
	r, err := p.ask(ctx, pr, checkPrompt, pr.Brief, true)
	v := walkthrough.Verdict{Control: r.control, Area: r.area}
	if r.revised {
		v.Step = r.step
	}
	return v, err
}

// asked is what the model did about a step.
type asked struct {
	step    walkthrough.Step
	revised bool
	control *walkthrough.Control
	// area holds what it drew, in desktop coordinates.
	area contracts.Rect
}

// ask sends a new screenshot and the step to the model and runs its
// overlay, revise_step and (with control) walkthrough_control calls.
func (p *Presenter) ask(ctx context.Context, pr walkthrough.Presentation, prompt, request string, control bool) (r asked, err error) {
	o := p.Orch
	r.step = pr.Step
	if o.Capturer == nil {
		return r, nil
	}
	caps, err := o.Capturer.Capture(ctx, o.Target)
	if err != nil {
		return r, fmt.Errorf("step capture: %w", err)
	}
	// The chat is not told: these screenshots belong to no request.
	images, primary, err := o.prepare(pr.Ref, caps, false)
	releaseAll(caps)
	if err != nil || primary == "" {
		return r, err
	}
	meta, _ := o.captureMeta(primary)
	provider, err := o.Provider(ctx)
	if err != nil {
		return r, fmt.Errorf("step provider: %w", err)
	}
	user := llm.Message{Role: llm.RoleUser, Content: []llm.Part{llm.Text(request)}}
	for _, img := range images {
		user.Content = append(user.Content, llm.ImagePart(img))
	}
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: []llm.Part{llm.Text(prompt)}}, user}
	specs := []overlay.ToolSpec{walkthrough.ReviseTool()}
	if o.Overlay != nil {
		specs = append(overlay.Tools(), specs...)
	}
	if control {
		specs = append(specs, walkthrough.ControlTool())
	}
	var tools []llm.Tool
	for _, t := range specs {
		tools = append(tools, llm.Tool{Name: t.Name, Description: t.Description, Schema: t.Schema})
	}
	quiet := &speechPipe{}
	noEmit := func(session.Event) {}
	var drawn []overlay.Command
	// Also what was drawn before a failure is on screen.
	defer func() { r.area = hintArea(meta, drawn) }()
	for round := 0; round < stepToolRounds && r.control == nil; round++ {
		res, err := o.stream(ctx, provider, llm.ChatRequest{Messages: msgs, Tools: tools, Policy: o.Policy, Session: pr.Ref}, noEmit, quiet)
		if err != nil {
			return r, err
		}
		if len(res.calls) == 0 {
			break
		}
		assistant := llm.Message{Role: llm.RoleAssistant, ToolCalls: res.calls}
		if res.thinking.Text != "" || res.thinking.Signature != "" {
			assistant.Content = append(assistant.Content, res.thinking)
		}
		msgs = append(msgs, assistant)
		for _, call := range res.calls {
			if err := ctx.Err(); err != nil {
				return r, err
			}
			switch {
			case call.Name == walkthrough.ToolRevise:
				rev, err := walkthrough.FromReviseCall(call.Arguments)
				if err != nil {
					msgs = append(msgs, toolResult(call, err.Error(), true))
					continue
				}
				r.step.Title, r.step.Say, r.revised = rev.Title, rev.Say, true
				msgs = append(msgs, toolResult(call, "revised", false))
			case call.Name == walkthrough.ToolControl && control:
				c, err := walkthrough.FromControlCall(call.Arguments, pr.Total)
				if err != nil {
					msgs = append(msgs, toolResult(call, err.Error(), true))
					continue
				}
				r.control = &c
				msgs = append(msgs, toolResult(call, "done", false))
			default:
				msg, cmds := o.runToolCmds(ctx, pr.Ref, primary, call)
				drawn = append(drawn, cmds...)
				msgs = append(msgs, msg)
			}
		}
	}
	return r, nil
}

// Say speaks a step.
func (p *Presenter) Say(ctx context.Context, text string) error {
	if p.Orch.Speaker == nil {
		return nil
	}
	return p.Orch.Speaker.Say(ctx, text, func() {})
}

// End removes the walkthrough's hints.
func (p *Presenter) End(ctx context.Context, ref contracts.SessionRef) {
	if p.Scene != nil {
		p.Scene.EndSession(ctx, ref)
	}
}

// hintArea is the desktop area the shown hints cover, labels aside (they
// sit beside what they name); zero for none or unusable metadata.
func hintArea(meta contracts.CaptureMetadata, cmds []overlay.Command) contracts.Rect {
	aspect := 1.0
	if meta.Native.W > 0 && meta.Native.H > 0 {
		aspect = float64(meta.Native.W) / float64(meta.Native.H)
	}
	lo := contracts.Point{X: math.Inf(1), Y: math.Inf(1)}
	hi := contracts.Point{X: math.Inf(-1), Y: math.Inf(-1)}
	for _, c := range cmds {
		if c.Op != overlay.OpShow || c.Kind == overlay.KindLabel {
			continue
		}
		// Radius is a share of the capture's width.
		rx, ry := c.Radius, c.Radius*aspect
		for _, pt := range c.Points {
			lo.X, lo.Y = min(lo.X, pt.X-rx), min(lo.Y, pt.Y-ry)
			hi.X, hi.Y = max(hi.X, pt.X+rx), max(hi.Y, pt.Y+ry)
		}
	}
	if lo.X > hi.X {
		return contracts.Rect{}
	}
	clamp := func(v float64) float64 { return min(1, max(0, v)) }
	a, err := coordinates.NormalizedToDesktop(meta, contracts.Point{X: clamp(lo.X), Y: clamp(lo.Y)})
	if err != nil {
		return contracts.Rect{}
	}
	b, err := coordinates.NormalizedToDesktop(meta, contracts.Point{X: clamp(hi.X), Y: clamp(hi.Y)})
	if err != nil {
		return contracts.Rect{}
	}
	// Rotation may swap the corners.
	return contracts.Rect{
		Min: contracts.Point{X: min(a.X, b.X), Y: min(a.Y, b.Y)},
		Max: contracts.Point{X: max(a.X, b.X), Y: max(a.Y, b.Y)},
	}
}

const stepPrompt = "You help a user through a task on their screen, one step at a time. The attached screenshot " +
	"shows the screen right now. Mark where the user has to act for the current step with point_at or annotate, " +
	"as fractions of the screenshot; mark only what is visible. If the screen shows the step must be done " +
	"differently than planned, call revise_step with a corrected title and instruction in the plan's language. " +
	"Do not write any other text."

// checkPrompt asks about a step after the screen around it changed.
const checkPrompt = "You help a user through a task on their screen, one step at a time. The screen around the " +
	"current step has just changed; the attached screenshot shows it now. Decide whether the user has done the " +
	"current step. If they have, call walkthrough_control with action next. If they did something else instead, " +
	"mark where to act now with point_at or annotate, or call revise_step when the step must be done differently. " +
	"If the step is simply not done yet, call nothing. Do not write any other text."

func stepRequest(pr walkthrough.Presentation) string {
	s := fmt.Sprintf("Goal: %q. Current step %d of %d: %q. Planned instruction: %q.",
		pr.Goal, pr.Index+1, pr.Total, pr.Step.Title, pr.Step.Say)
	if len(pr.Step.Shapes) > 0 {
		s += " Planned marks, on an older screenshot: " + string(pr.Step.Shapes)
	}
	return s
}
