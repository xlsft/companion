package overlay

import (
	"errors"
	"testing"

	"github.com/xlsft/openclick/pkg/contracts"
)

func show(kind Kind, pts ...contracts.Point) Command {
	return Command{
		Op:      OpShow,
		ID:      "a1",
		Session: contracts.SessionRef{ID: "s1", Generation: 1},
		Capture: "c1",
		Kind:    kind,
		Points:  pts,
		Segment: -1,
	}
}

func TestValidate(t *testing.T) {
	p := contracts.Point{X: 0.5, Y: 0.5}
	circle := show(KindCircle, p)
	circle.Radius = 0.1
	label := show(KindLabel, p)
	label.Text = "Save"

	tests := []struct {
		name  string
		cmd   Command
		valid bool
	}{
		{"point", show(KindPoint, p), true},
		{"circle", circle, true},
		{"label", label, true},
		{"rect", show(KindRect, contracts.Point{X: 0.1, Y: 0.1}, contracts.Point{X: 0.2, Y: 0.3}), true},
		{"arrow", show(KindArrow, contracts.Point{}, contracts.Point{X: 1, Y: 1}), true},
		{"freehand", show(KindFreehand, p, p, p), true},
		{"clear", Command{Op: OpClearSession, Session: contracts.SessionRef{ID: "s1"}}, true},

		{"point outside capture", show(KindPoint, contracts.Point{X: 1.2, Y: 0.5}), false},
		{"circle without radius", show(KindCircle, p), false},
		{"label without text", show(KindLabel, p), false},
		{"rect reversed", show(KindRect, contracts.Point{X: 0.5, Y: 0.5}, contracts.Point{X: 0.1, Y: 0.1}), false},
		{"arrow one point", show(KindArrow, p), false},
		{"freehand one point", show(KindFreehand, p), false},
		{"unknown kind", show("blink", p), false},
		{"no session", Command{Op: OpClearSession}, false},
		{"no capture", func() Command { c := show(KindPoint, p); c.Capture = ""; return c }(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cmd.Validate()
			if tt.valid && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("error = %v, want ErrInvalidCommand", err)
			}
		})
	}
}
