package ui

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"

	"github.com/xlsft/companion/internal/chat"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/walkthrough"
)

type fakeGuide struct {
	views chan walkthrough.View
	mu    sync.Mutex
	cmds  []string
}

func (f *fakeGuide) Subscribe() (<-chan walkthrough.View, func()) { return f.views, func() {} }
func (f *fakeGuide) Do(_ context.Context, c walkthrough.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, string(c))
	return nil
}
func (f *fakeGuide) done() string { f.mu.Lock(); defer f.mu.Unlock(); return strings.Join(f.cmds, ",") }

func TestGuideCardControls(t *testing.T) {
	running := walkthrough.View{Status: walkthrough.StatusRunning, Goal: "Export", Index: 1, Total: 3, Title: "Open Export", Text: "Open the panel.", Prev: "Select"}
	first := running
	first.Index, first.Prev = 0, ""
	paused := running
	paused.Status = walkthrough.StatusPaused
	watching := running
	watching.Watching = true
	tests := []struct {
		name  string
		view  walkthrough.View
		click func(c *guideCard)
		want  string
	}{
		{"next", running, func(c *guideCard) { c.nextBtn.Click() }, "next"},
		{"next while watching for the user", watching, func(c *guideCard) { c.nextBtn.Click() }, "next"},
		{"previous", running, func(c *guideCard) { c.prevBtn.Click() }, "previous"},
		{"previous on the first step is disabled", first, func(c *guideCard) { c.prevBtn.Click() }, ""},
		{"repeat", running, func(c *guideCard) { c.repeatBtn.Click() }, "repeat"},
		{"pause", running, func(c *guideCard) { c.pauseBtn.Click() }, "pause"},
		{"resume when paused", paused, func(c *guideCard) { c.pauseBtn.Click() }, "resume"},
		{"end", running, func(c *guideCard) { c.end.Click() }, "stop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, m, p := homeWith(t, newFakeChat(chat.Capabilities{Text: true}), nil)
			g := &fakeGuide{views: make(chan walkthrough.View, 1)}
			p.card.g = newGuideModel(g, slog.New(slog.DiscardHandler), func() {})
			th := theme.New(false, 1)
			r := new(input.Router)
			g.views <- tt.view
			waitFor(t, r, h, th, m, "card", p.card.active)
			tt.click(&p.card)
			frame(r, h.sh, th, m)
			if tt.want == "" {
				time.Sleep(20 * time.Millisecond)
				if got := g.done(); got != "" {
					t.Fatalf("sent %q", got)
				}
				return
			}
			waitFor(t, r, h, th, m, tt.want, func() bool { return g.done() == tt.want })
		})
	}
}

// A finished walkthrough leaves no card behind.
func TestGuideCardHidesWhenOver(t *testing.T) {
	h, m, p := homeWith(t, newFakeChat(chat.Capabilities{Text: true}), nil)
	g := &fakeGuide{views: make(chan walkthrough.View, 1)}
	p.card.g = newGuideModel(g, slog.New(slog.DiscardHandler), func() {})
	th := theme.New(true, 1)
	r := new(input.Router)
	g.views <- walkthrough.View{Status: walkthrough.StatusRunning, Goal: "g", Total: 2, Title: "t", Text: "x"}
	waitFor(t, r, h, th, m, "card", p.card.active)
	g.views <- walkthrough.View{Status: walkthrough.StatusDone, Goal: "g", Index: 1, Total: 2}
	waitFor(t, r, h, th, m, "no card", func() bool { return !p.card.active() })
}
