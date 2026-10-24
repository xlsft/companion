package talk

import (
	"context"

	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/pkg/contracts"
)

// Session is the part of *session.Manager user inputs go to.
type Session interface {
	Post(session.Event) error
	Subscribe() (<-chan session.Snapshot, func())
}

// Scene is the part of overlay.Engine that cancellation clears.
type Scene interface {
	Clear(ctx context.Context)
	EndSession(ctx context.Context, ref contracts.SessionRef)
}

// Control is the input path of every Talk request, from hotkeys and the
// chat alike (OC-65). Ending a generation cancels its context, which
// stops the recording, the model stream, playback and tool calls; the
// overlay is a separate scene, so Control clears it as well: at once on
// Escape, and for every generation that ends cancelled, interrupted by a
// new request or failed. A finished answer keeps its hints for their
// lifetime. Escape also stops a walkthrough in progress.
type Control struct {
	Session Session
	Scene   Scene // nil without an overlay
	Guide   Stopper
}

// Stopper ends a walkthrough in progress and removes its hints.
type Stopper interface{ Stop(ctx context.Context) }

// Post forwards ev to the session. Cancel aimed at whatever is active
// also clears the overlay even when no session runs, so Escape always
// removes what is on screen.
func (c *Control) Post(ev session.Event) error {
	err := c.Session.Post(ev)
	if cv, ok := ev.(session.Cancel); ok && cv.Ref == (contracts.SessionRef{}) {
		if c.Guide != nil {
			c.Guide.Stop(context.Background())
		}
		if c.Scene != nil {
			c.Scene.Clear(context.Background())
		}
	}
	return err
}

func (c *Control) Subscribe() (<-chan session.Snapshot, func()) { return c.Session.Subscribe() }

// Run follows the session until ctx ends or the session closes.
func (c *Control) Run(ctx context.Context) {
	if c.Scene == nil {
		return
	}
	snaps, unsub := c.Session.Subscribe()
	defer unsub()
	// Clearing must happen even while the app shuts down.
	clearCtx := context.WithoutCancel(ctx)
	for {
		select {
		case s, ok := <-snaps:
			if !ok {
				return
			}
			switch s.State {
			case session.StateCancelled, session.StateInterrupted, session.StateFailed:
				c.Scene.EndSession(clearCtx, s.Ref)
			}
		case <-ctx.Done():
			return
		}
	}
}
