package ui

import (
	"context"
	"log/slog"
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/walkthrough"
)

// GuideBackend runs walkthroughs (OC-68); *walkthrough.Service implements
// it. Without one (Options.Guide nil) the chat shows no walkthrough card.
type GuideBackend interface {
	// Subscribe delivers the walkthrough in progress and every change.
	Subscribe() (<-chan walkthrough.View, func())
	Do(ctx context.Context, cmd walkthrough.Command) error
}

var _ GuideBackend = (*walkthrough.Service)(nil)

// guideModel follows the walkthrough off the UI goroutine, like chatModel.
type guideModel struct {
	be         GuideBackend
	log        *slog.Logger
	invalidate func()

	mu      sync.Mutex
	started bool
	closed  bool
	unsub   func()
	view    walkthrough.View
	failed  bool
}

func newGuideModel(be GuideBackend, log *slog.Logger, invalidate func()) *guideModel {
	return &guideModel{be: be, log: log, invalidate: invalidate}
}

func (g *guideModel) ensure() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.be == nil || g.started || g.closed {
		return
	}
	g.started = true
	ch, unsub := g.be.Subscribe()
	g.unsub = unsub
	go func() {
		for v := range ch {
			g.mu.Lock()
			g.view = v
			g.mu.Unlock()
			g.invalidate()
		}
	}()
}

func (g *guideModel) snapshot() (walkthrough.View, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.view, g.failed
}

// do sends a command off the UI goroutine; a failure is logged by code
// and shown on the card.
func (g *guideModel) do(cmd walkthrough.Command) {
	if g.be == nil {
		return
	}
	g.mu.Lock()
	g.failed = false
	g.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), chatOpTimeout)
		defer cancel()
		if err := g.be.Do(ctx, cmd); err != nil {
			g.log.Warn("walkthrough action failed", "cmd", string(cmd), "err", err)
			g.mu.Lock()
			g.failed = true
			g.mu.Unlock()
			g.invalidate()
		}
	}()
}

func (g *guideModel) close() {
	g.mu.Lock()
	g.closed = true
	unsub := g.unsub
	g.unsub = nil
	g.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

// guideCard is the walkthrough card above the composer: which step of
// how many, its title and instruction, the step done before it, and the
// controls the voice commands also reach.
type guideCard struct {
	g                                          *guideModel
	prevBtn, repeatBtn, pauseBtn, nextBtn, end widget.Clickable
}

func (c *guideCard) active() bool {
	c.g.ensure()
	v, _ := c.g.snapshot()
	return v.Active()
}

func (c *guideCard) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	c.g.ensure()
	v, failed := c.g.snapshot()
	if !v.Active() {
		return layout.Dimensions{}
	}
	paused := v.Status == walkthrough.StatusPaused
	for _, b := range []struct {
		btn *widget.Clickable
		cmd walkthrough.Command
	}{{&c.prevBtn, walkthrough.CmdPrev}, {&c.repeatBtn, walkthrough.CmdRepeat}, {&c.nextBtn, walkthrough.CmdNext}, {&c.end, walkthrough.CmdStop}} {
		// Nothing comes before the first step; its button is disabled.
		if b.btn.Clicked(gtx) && (b.cmd != walkthrough.CmdPrev || v.Index > 0) {
			c.g.do(b.cmd)
		}
	}
	if c.pauseBtn.Clicked(gtx) {
		cmd := walkthrough.CmdPause
		if paused {
			cmd = walkthrough.CmdResume
		}
		c.g.do(cmd)
	}

	head := func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(th.Dp(32))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				t := kit.Text(th, 12, Tf(l, "guide.step", v.Index+1, v.Total)+" · "+v.Goal, th.Pal.AccentText)
				t.MaxLines = 1
				return kit.VCenter(gtx, h, t.Layout)
			}),
			kit.HSpace(th.Dp(12)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return kit.StepTrack(gtx, th, v.Index, v.Total) })
			}),
			kit.HSpace(th.Dp(4)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.IconButton(gtx, th, &c.end, icon.Close, T(l, "guide.stop"))
			}),
		)
	}
	body := func(gtx layout.Context) layout.Dimensions {
		rows := []layout.Widget{kit.Title(th, v.Title).Layout, kit.Lead(th, v.Text).Layout}
		if v.Prev != "" {
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return kit.Badge(gtx, th, theme.StatusOK, Tf(l, "guide.prevDone", v.Index, v.Prev))
			})
		}
		switch {
		case failed:
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return kit.Alert(gtx, th, theme.StatusError, T(l, "guide.error"), "")
			})
		case v.Preparing:
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return metaRow(gtx, th, icon.Loading3, T(l, "guide.preparing"), th.Pal.Muted)
			})
		case v.Watching:
			// Auto-advance is armed: doing the step moves the card on.
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return metaRow(gtx, th, icon.Eye, T(l, "guide.watching"), th.Pal.Muted)
			})
		}
		return kit.VStack(th, 10, rows...)(gtx)
	}
	controls := func(gtx layout.Context) layout.Dimensions {
		pauseLabel, pauseIcon := T(l, "guide.pause"), icon.PauseFill
		if paused {
			pauseLabel, pauseIcon = T(l, "guide.resume"), icon.PlayFill
		}
		nextLabel := T(l, "guide.next")
		if v.Index == v.Total-1 {
			nextLabel = T(l, "guide.finish")
		}
		return kit.Spread(
			kit.HStack(th, 6,
				func(gtx layout.Context) layout.Dimensions {
					if v.Index == 0 {
						gtx = gtx.Disabled()
					}
					return kit.IconButton(gtx, th, &c.prevBtn, icon.Left, T(l, "guide.prev"))
				},
				func(gtx layout.Context) layout.Dimensions {
					return kit.ButtonStyle{Kind: kit.Secondary, Label: T(l, "guide.repeat"), Icon: icon.Refresh2, Small: true}.Layout(gtx, th, &c.repeatBtn)
				},
				func(gtx layout.Context) layout.Dimensions {
					return kit.ButtonStyle{Kind: kit.Secondary, Label: pauseLabel, Icon: pauseIcon, Small: true}.Layout(gtx, th, &c.pauseBtn)
				},
			),
			func(gtx layout.Context) layout.Dimensions {
				return kit.ButtonStyle{Kind: kit.Primary, Label: nextLabel, Icon: icon.ArrowRight, Small: true}.Layout(gtx, th, &c.nextBtn)
			},
		)(gtx)
	}
	return kit.Rows(gtx, th,
		kit.Pad(th, 0, 16, kit.VStack(th, 6,
			kit.Pad(th, 6, 0, head),
			func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: th.Dp(14)}.Layout(gtx, body)
			})),
		kit.Pad(th, 10, 16, controls),
	)
}
