package walkthrough

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/pkg/contracts"
)

// Tool names exposed to the model.
const (
	// ToolStart starts a walkthrough from the model's plan.
	ToolStart = "start_walkthrough"
	// ToolRevise lets the model correct the current step after looking at
	// a new screenshot of it.
	ToolRevise = "revise_step"
)

// StartTool is the start_walkthrough definition.
func StartTool() overlay.ToolSpec {
	return overlay.ToolSpec{
		Name: ToolStart,
		Description: "Guide the user through a task that takes several actions on screen, one step at a time. " +
			"Use it when the user asks how to do something that needs more than one or two clicks. Give the goal " +
			"and the ordered steps; each step has a short title, what to say for it, and optional shapes marking " +
			"where to act. Shapes use the annotate format, as fractions (0..1) of the attached screenshot; only " +
			"the first step's shapes can match the current screen, later steps are re-checked on a new screenshot " +
			"when the user gets there. After calling it say nothing more: the steps are shown and spoken for you.",
		Schema: json.RawMessage(startSchema),
	}
}

// ReviseTool is the revise_step definition.
func ReviseTool() overlay.ToolSpec {
	return overlay.ToolSpec{
		Name:        ToolRevise,
		Description: "Change the current walkthrough step when the new screenshot shows it must be done differently than planned.",
		Schema:      json.RawMessage(reviseSchema),
	}
}

const stepFields = `"title": {"type": "string", "minLength": 1, "maxLength": 80, "description": "Short step name, e.g. 'Open the Fill panel'"},
        "say": {"type": "string", "minLength": 1, "maxLength": 400, "description": "One or two short spoken sentences: what to do now"}`

var startSchema = `{
  "type": "object",
  "properties": {
    "goal": {"type": "string", "minLength": 1, "maxLength": 120, "description": "What the user will have done at the end"},
    "steps": {
      "type": "array",
      "minItems": 1,
      "maxItems": 12,
      "items": {
        "type": "object",
        "properties": {
        ` + stepFields + `,
        "shapes": {"type": "array", "maxItems": 12, "items": ` + overlay.ShapeSchema + `}
        },
        "required": ["title", "say"],
        "additionalProperties": false
      }
    }
  },
  "required": ["goal", "steps"],
  "additionalProperties": false
}`

var reviseSchema = `{
  "type": "object",
  "properties": {
        ` + stepFields + `
  },
  "required": ["title", "say"],
  "additionalProperties": false
}`

// FromToolCall validates a start_walkthrough call into a plan. withScreen
// says whether the request carried a screenshot; without one no step may
// have shapes, as there is nothing they could be normalized to.
func FromToolCall(args json.RawMessage, withScreen bool) (Plan, error) {
	var p Plan
	if err := decodeStrict(args, &p); err != nil {
		return Plan{}, err
	}
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	for i := range p.Steps {
		sh := p.Steps[i].Shapes
		if len(bytes.TrimSpace(sh)) == 0 || bytes.Equal(bytes.TrimSpace(sh), []byte("null")) || bytes.Equal(bytes.TrimSpace(sh), []byte("[]")) {
			p.Steps[i].Shapes = nil
			continue
		}
		if !withScreen {
			return Plan{}, fmt.Errorf("%w: step %d has shapes but no screenshot is attached", ErrInvalidPlan, i)
		}
		if _, err := ShapeCommands(overlay.CallContext{
			Session: contracts.SessionRef{ID: "validate"}, Capture: "validate", CallID: "validate",
		}, sh); err != nil {
			return Plan{}, fmt.Errorf("%w: step %d: %v", ErrInvalidPlan, i, err)
		}
	}
	return p, nil
}

// Revision is a revise_step call.
type Revision struct {
	Title string `json:"title"`
	Say   string `json:"say"`
}

// FromReviseCall validates a revise_step call.
func FromReviseCall(args json.RawMessage) (Revision, error) {
	var r Revision
	if err := decodeStrict(args, &r); err != nil {
		return Revision{}, err
	}
	if err := (Plan{Goal: "-", Steps: []Step{{Title: r.Title, Say: r.Say}}}).Validate(); err != nil {
		return Revision{}, err
	}
	return r, nil
}

// ShapeCommands turns a step's shapes into overlay commands on cc's
// screenshot, with the annotate tool's validation.
func ShapeCommands(cc overlay.CallContext, shapes json.RawMessage) ([]overlay.Command, error) {
	if len(shapes) == 0 {
		return nil, nil
	}
	args, err := json.Marshal(struct {
		Shapes json.RawMessage `json:"shapes"`
	}{shapes})
	if err != nil {
		return nil, err
	}
	return overlay.FromToolCall(cc, overlay.ToolAnnotate, args)
}

func decodeStrict(args json.RawMessage, v any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPlan, err)
	}
	return nil
}
