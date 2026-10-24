package talk

import (
	"context"
	"fmt"

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
	Do(ctx context.Context, cmd walkthrough.Command) error
	// Hush stops the step's speech; the walkthrough stays where it is.
	Hush()
}

var _ Guide = (*walkthrough.Service)(nil)

// stepToolRounds bounds the model rounds that re-mark one step.
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
func (p *Presenter) Show(ctx context.Context, pr walkthrough.Presentation) (walkthrough.Step, error) {
	o := p.Orch
	step := pr.Step
	if o.Overlay == nil {
		return step, nil
	}
	if pr.Capture != "" {
		cmds, err := walkthrough.ShapeCommands(overlay.CallContext{Session: pr.Ref, Capture: pr.Capture, CallID: "step", Segment: -1}, step.Shapes)
		if err != nil {
			return step, err
		}
		for _, c := range cmds {
			if err := o.Overlay.Apply(ctx, c); err != nil {
				return step, err
			}
		}
		return step, nil
	}
	if o.Capturer == nil {
		return step, nil
	}
	caps, err := o.Capturer.Capture(ctx, o.Target)
	if err != nil {
		return step, fmt.Errorf("step capture: %w", err)
	}
	// The chat is not told: these screenshots belong to no request.
	images, primary, err := o.prepare(pr.Ref, caps, false)
	releaseAll(caps)
	if err != nil || primary == "" {
		return step, err
	}
	provider, err := o.Provider(ctx)
	if err != nil {
		return step, fmt.Errorf("step provider: %w", err)
	}
	user := llm.Message{Role: llm.RoleUser, Content: []llm.Part{llm.Text(stepRequest(pr))}}
	for _, img := range images {
		user.Content = append(user.Content, llm.ImagePart(img))
	}
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: []llm.Part{llm.Text(stepPrompt)}}, user}
	var tools []llm.Tool
	for _, t := range append(overlay.Tools(), walkthrough.ReviseTool()) {
		tools = append(tools, llm.Tool{Name: t.Name, Description: t.Description, Schema: t.Schema})
	}
	quiet := &speechPipe{}
	noEmit := func(session.Event) {}
	for round := 0; round < stepToolRounds; round++ {
		res, err := o.stream(ctx, provider, llm.ChatRequest{Messages: msgs, Tools: tools, Policy: o.Policy, Session: pr.Ref}, noEmit, quiet)
		if err != nil {
			return step, err
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
				return step, err
			}
			if call.Name == walkthrough.ToolRevise {
				r, err := walkthrough.FromReviseCall(call.Arguments)
				if err != nil {
					msgs = append(msgs, toolResult(call, err.Error(), true))
					continue
				}
				step.Title, step.Say = r.Title, r.Say
				msgs = append(msgs, toolResult(call, "revised", false))
				continue
			}
			msgs = append(msgs, o.runTool(ctx, pr.Ref, primary, call))
		}
	}
	return step, nil
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

const stepPrompt = "You help a user through a task on their screen, one step at a time. The attached screenshot " +
	"shows the screen right now. Mark where the user has to act for the current step with point_at or annotate, " +
	"as fractions of the screenshot; mark only what is visible. If the screen shows the step must be done " +
	"differently than planned, call revise_step with a corrected title and instruction in the plan's language. " +
	"Do not write any other text."

func stepRequest(pr walkthrough.Presentation) string {
	s := fmt.Sprintf("Goal: %q. Current step %d of %d: %q. Planned instruction: %q.",
		pr.Goal, pr.Index+1, pr.Total, pr.Step.Title, pr.Step.Say)
	if len(pr.Step.Shapes) > 0 {
		s += " Planned marks, on an older screenshot: " + string(pr.Step.Shapes)
	}
	return s
}
