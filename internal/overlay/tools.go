package overlay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// Tool names exposed to the model (OC-51). Models point and draw only
// through these typed tools (FR-OVERLAY-004).
const (
	ToolPointAt  = "point_at"
	ToolAnnotate = "annotate"
	ToolClear    = "clear_annotations"
)

// ToolSpec is a provider-neutral tool definition; the LLM gateway converts
// it into OpenAI or Anthropic tool format.
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Tools returns the overlay tool definitions. Coordinates are normalized to
// the attached screenshot so the model never needs to know screen pixels
// or DPI (FR-OVERLAY-005).
func Tools() []ToolSpec {
	return []ToolSpec{
		{
			Name: ToolPointAt,
			Description: "Point at one UI element on the attached screenshot: moves a virtual cursor there " +
				"and draws a ring around it. x and y are the element center as fractions of the " +
				"screenshot width and height (0..1, origin top-left).",
			Schema: json.RawMessage(pointAtSchema),
		},
		{
			Name: ToolAnnotate,
			Description: "Draw shapes on top of the screen to explain something. All coordinates are fractions " +
				"(0..1) of the attached screenshot, origin top-left.",
			Schema: json.RawMessage(annotateSchema),
		},
		{
			Name:        ToolClear,
			Description: "Remove all annotations you drew in this answer.",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
	}
}

const pointAtSchema = `{
  "type": "object",
  "properties": {
    "x": {"type": "number", "minimum": 0, "maximum": 1},
    "y": {"type": "number", "minimum": 0, "maximum": 1},
    "radius": {"type": "number", "exclusiveMinimum": 0, "maximum": 0.5, "description": "Ring radius as a fraction of screenshot width; default 0.02"},
    "label": {"type": "string", "maxLength": 80, "description": "Short label shown next to the ring"}
  },
  "required": ["x", "y"],
  "additionalProperties": false
}`

// ShapeSchema is the JSON schema of one annotate shape; tools that carry
// drawings of their own (walkthrough steps) reuse it so every drawing the
// model makes is validated the same way.
const ShapeSchema = `{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["ring", "rect", "circle", "arrow", "freehand", "label", "spotlight", "highlight", "underline", "brackets", "badge"]},
    "points": {
      "type": "array",
      "items": {"type": "array", "items": {"type": "number", "minimum": 0, "maximum": 1}, "minItems": 2, "maxItems": 2},
      "description": "ring/circle/spotlight/badge/label: [center or anchor]; rect/highlight/brackets: [top-left, bottom-right]; arrow/underline: [from, to]; freehand: 2..64 points. spotlight dims everything but a circle; highlight is a marker over text; brackets are focus corners; badge is a numbered step marker whose text is 1-3 characters"
    },
    "radius": {"type": "number", "exclusiveMinimum": 0, "maximum": 0.5},
    "text": {"type": "string", "maxLength": 80},
    "color": {"type": "string", "pattern": "^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$"},
    "dashed": {"type": "boolean", "description": "circle only: dashed outline for secondary marks, e.g. several cells to click"}
  },
  "required": ["kind", "points"],
  "additionalProperties": false
}`

const annotateSchema = `{
  "type": "object",
  "properties": {
    "shapes": {"type": "array", "minItems": 1, "maxItems": 12, "items": ` + ShapeSchema + `}
  },
  "required": ["shapes"],
  "additionalProperties": false
}`

const (
	defaultRingRadius      = 0.02
	defaultSpotlightRadius = 0.06
	maxFreehandPoints      = 64
	maxShapes              = 12
)

// MaxShapes is how many shapes one drawing may have.
const MaxShapes = maxShapes

var ErrBadToolCall = errors.New("bad overlay tool call")

// CallContext is supplied by the orchestrator, never by the model: the
// model cannot choose which session or capture it draws on.
type CallContext struct {
	Session contracts.SessionRef
	Capture contracts.CaptureID
	// CallID is the provider's tool call id; command ids derive from it so
	// a repeated call replaces its own drawing instead of duplicating it.
	CallID  string
	Segment int
	// Lifetime for created annotations; 0 keeps them until the session
	// ends or the segment changes.
	Lifetime time.Duration
}

// FromToolCall converts a model tool call into validated overlay commands.
func FromToolCall(cc CallContext, name string, args json.RawMessage) ([]Command, error) {
	if cc.Capture == "" && name != ToolClear {
		return nil, fmt.Errorf("%w: no screenshot attached to this request", ErrBadToolCall)
	}
	var cmds []Command
	switch name {
	case ToolPointAt:
		var a struct {
			X, Y   *float64
			Radius float64
			Label  string
		}
		if err := decodeStrict(args, &a); err != nil {
			return nil, err
		}
		if a.X == nil || a.Y == nil {
			return nil, fmt.Errorf("%w: x and y are required", ErrBadToolCall)
		}
		p := contracts.Point{X: *a.X, Y: *a.Y}
		r := a.Radius
		if r == 0 {
			r = defaultRingRadius
		}
		cmds = append(cmds,
			cc.show(cc.CallID+"/cursor", Command{Kind: KindPoint, Points: []contracts.Point{p}}),
			cc.show(cc.CallID+"/ring", Command{Kind: KindRing, Points: []contracts.Point{p}, Radius: r, Style: Style{Emphasis: true}}),
		)
		if a.Label != "" {
			// Anchor the label just past the ring's glow; the renderer keeps
			// it on screen near the right edge.
			lp := contracts.Point{X: min(p.X+r*1.3, 1), Y: p.Y}
			cmds = append(cmds, cc.show(cc.CallID+"/label", Command{Kind: KindLabel, Points: []contracts.Point{lp}, Text: a.Label}))
		}
	case ToolAnnotate:
		var a struct {
			Shapes []struct {
				Kind   Kind
				Points [][]float64
				Radius float64
				Text   string
				Color  string
				Dashed bool
			}
		}
		if err := decodeStrict(args, &a); err != nil {
			return nil, err
		}
		if len(a.Shapes) == 0 || len(a.Shapes) > maxShapes {
			return nil, fmt.Errorf("%w: need 1..%d shapes", ErrBadToolCall, maxShapes)
		}
		for i, s := range a.Shapes {
			switch s.Kind {
			case KindRing, KindRect, KindCircle, KindArrow, KindFreehand, KindLabel,
				KindSpotlight, KindHighlight, KindUnderline, KindBrackets, KindBadge:
			default:
				return nil, fmt.Errorf("%w: shape %d kind %q not allowed", ErrBadToolCall, i, s.Kind)
			}
			if len(s.Points) > maxFreehandPoints {
				return nil, fmt.Errorf("%w: shape %d has too many points", ErrBadToolCall, i)
			}
			pts := make([]contracts.Point, len(s.Points))
			for j, xy := range s.Points {
				if len(xy) != 2 {
					return nil, fmt.Errorf("%w: shape %d point %d is not [x,y]", ErrBadToolCall, i, j)
				}
				pts[j] = contracts.Point{X: xy[0], Y: xy[1]}
			}
			r := s.Radius
			if r == 0 {
				switch s.Kind {
				case KindRing, KindCircle:
					r = defaultRingRadius
				case KindSpotlight:
					r = defaultSpotlightRadius
				}
			}
			cmds = append(cmds, cc.show(fmt.Sprintf("%s/%d", cc.CallID, i), Command{
				Kind: s.Kind, Points: pts, Radius: r, Text: s.Text, Style: Style{Color: s.Color, Dashed: s.Dashed && s.Kind == KindCircle},
			}))
		}
	case ToolClear:
		return []Command{{Op: OpClearSession, Session: cc.Session}}, nil
	default:
		return nil, fmt.Errorf("%w: unknown tool %q", ErrBadToolCall, name)
	}
	for _, c := range cmds {
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadToolCall, err)
		}
	}
	return cmds, nil
}

func (cc CallContext) show(id string, c Command) Command {
	c.Op = OpShow
	c.ID = id
	c.Session = cc.Session
	c.Capture = cc.Capture
	c.Segment = cc.Segment
	c.Lifetime = cc.Lifetime
	return c
}

func decodeStrict(args json.RawMessage, v any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrBadToolCall, err)
	}
	return nil
}
