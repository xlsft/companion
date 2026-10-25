package kit

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/xlsft/companion/internal/ui/theme"
)

func TestTypeAheadMatch(t *testing.T) {
	labels := []string{"claude-haiku", "gpt-4o", "GPT-5", "llama3:8b", "openai/gpt-oss"}
	at := func(i int) string { return labels[i] }
	tests := []struct {
		typed string
		want  int
	}{
		{"", -1},
		{"g", 1},
		{"gpt-5", 2},
		{"oss", 4}, // no prefix match: the first that contains it
		{"lla", 3},
		{"zzz", -1},
	}
	for _, tt := range tests {
		if got := TypeAheadMatch(at, len(labels), tt.typed); got != tt.want {
			t.Errorf("%q: got %d, want %d", tt.typed, got, tt.want)
		}
	}
}

// Typing in an open list focuses the first match, scrolled into view;
// a pause starts a new word.
func TestDropdownTypeAhead(t *testing.T) {
	th := theme.New(false, 1)
	var r input.Router
	var d Dropdown
	labels := make([]string, 60)
	for i := range labels {
		labels[i] = fmt.Sprintf("model-%02d", i)
	}
	labels[42] = "qwen-coder"
	labels[50] = "query-model"
	now := time.Now()
	frame := func() {
		ops := new(op.Ops)
		gtx := layout.Context{Ops: ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: now,
			Source: r.Source(), Constraints: layout.Exact(image.Pt(400, 800))}
		d.Update(gtx, len(labels))
		d.Layout(gtx, th, labels, 0, "")
		r.Frame(ops)
	}
	press := func(name key.Name) {
		r.Queue(key.Event{Name: name, State: key.Press})
		frame()
		frame()
	}
	d.SetOpen(true)
	d.focusOpts = true
	frame()
	frame()
	if !r.Source().Focused(&d.options[0]) {
		t.Fatal("open list does not focus the chosen option")
	}
	press("Q")
	press("U")
	if !r.Source().Focused(&d.options[50]) {
		t.Errorf("\"qu\" did not focus %q (typed %q)", labels[50], d.typed)
	}
	if p := d.list.Position; 50 < p.First || 50 >= p.First+p.Count {
		t.Errorf("match not scrolled into view: %+v", p)
	}
	now = now.Add(2 * typeAheadPause)
	press("Q")
	press("W")
	if !r.Source().Focused(&d.options[42]) {
		t.Errorf("new word \"qw\" did not focus %q (typed %q)", labels[42], d.typed)
	}
}
