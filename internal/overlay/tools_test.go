package overlay

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/xlsft/companion/pkg/contracts"
)

var cc = CallContext{Session: contracts.SessionRef{ID: "s", Generation: 3}, Capture: "cap", CallID: "call1", Segment: -1}

func TestToolSchemasAreValidJSON(t *testing.T) {
	for _, tool := range Tools() {
		var v map[string]any
		if err := json.Unmarshal(tool.Schema, &v); err != nil {
			t.Errorf("%s schema: %v", tool.Name, err)
		}
	}
}

func TestPointAt(t *testing.T) {
	cmds, err := FromToolCall(cc, ToolPointAt, json.RawMessage(`{"x":0.4,"y":0.6,"label":"Export"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 3 || cmds[0].Kind != KindPoint || cmds[1].Kind != KindRing || cmds[2].Kind != KindLabel {
		t.Fatalf("got %+v", cmds)
	}
	for _, c := range cmds {
		if c.Session != cc.Session || c.Capture != cc.Capture {
			t.Fatalf("command not bound to call context: %+v", c)
		}
	}
	if cmds[1].Radius != defaultRingRadius {
		t.Errorf("default radius = %g", cmds[1].Radius)
	}
}

func TestAnnotate(t *testing.T) {
	args := `{"shapes":[
		{"kind":"arrow","points":[[0.1,0.1],[0.5,0.5]]},
		{"kind":"rect","points":[[0.2,0.2],[0.3,0.4]],"color":"#00FF00"},
		{"kind":"circle","points":[[0.5,0.5]]}
	]}`
	cmds, err := FromToolCall(cc, ToolAnnotate, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 3 || cmds[1].Style.Color != "#00FF00" || cmds[2].Radius != defaultRingRadius {
		t.Fatalf("got %+v", cmds)
	}
	if cmds[0].ID == cmds[1].ID {
		t.Fatal("shape ids must be distinct")
	}
}

func TestBadToolCalls(t *testing.T) {
	tests := []struct {
		name, tool, args string
		ctx              CallContext
	}{
		{"missing y", ToolPointAt, `{"x":0.5}`, cc},
		{"out of range", ToolPointAt, `{"x":1.5,"y":0.5}`, cc},
		{"unknown field", ToolPointAt, `{"x":0.5,"y":0.5,"session":"other"}`, cc},
		{"no capture", ToolPointAt, `{"x":0.5,"y":0.5}`, CallContext{Session: cc.Session}},
		{"image card not allowed", ToolAnnotate, `{"shapes":[{"kind":"image_card","points":[[0.1,0.1]]}]}`, cc},
		{"bad point", ToolAnnotate, `{"shapes":[{"kind":"label","points":[[0.1]],"text":"x"}]}`, cc},
		{"no shapes", ToolAnnotate, `{"shapes":[]}`, cc},
		{"malformed json", ToolAnnotate, `{"shapes":`, cc},
		{"unknown tool", "click", `{}`, cc},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := FromToolCall(tt.ctx, tt.tool, json.RawMessage(tt.args)); !errors.Is(err, ErrBadToolCall) {
				t.Fatalf("err = %v, want ErrBadToolCall", err)
			}
		})
	}
}

func TestClearWorksWithoutCapture(t *testing.T) {
	cmds, err := FromToolCall(CallContext{Session: cc.Session}, ToolClear, nil)
	if err != nil || len(cmds) != 1 || cmds[0].Op != OpClearSession {
		t.Fatalf("got %+v, %v", cmds, err)
	}
}
