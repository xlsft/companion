package walkthrough

import (
	"context"
	"errors"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// armLocked starts watching the screen for the user doing the current
// step, around area (desktop coordinates; zero: the whole display).
func (s *Service) armLocked(area contracts.Rect) {
	s.disarmLocked()
	w := s.cfg.Watch
	if w == nil || w.Capturer == nil || (w.Enabled != nil && !w.Enabled()) {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.watchStop = cancel
	s.wgen++
	wgen, at := s.wgen, s.st.Index
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.watch(ctx, wgen, at, area)
		s.mu.Lock()
		if s.wgen == wgen && s.watchStop != nil {
			s.watchStop = nil
			s.publishLocked()
		}
		s.mu.Unlock()
	}()
	s.publishLocked()
}

// disarmLocked stops the current step's watcher.
func (s *Service) disarmLocked() {
	if s.watchStop != nil {
		s.watchStop()
		s.watchStop = nil
	}
}

func (s *Service) watchRequest(area contracts.Rect) contracts.CaptureRequest {
	if r := watchArea(area); r != (contracts.Rect{}) {
		return contracts.CaptureRequest{Target: contracts.CaptureRegion, Region: r}
	}
	if s.cfg.Watch.Target != nil {
		return s.cfg.Watch.Target()
	}
	return contracts.CaptureRequest{Target: contracts.CaptureActiveDisplay}
}

// watch samples the screen until ctx ends, the step's checks are used up
// or capturing keeps failing, and asks the model about settled changes.
// Every frame is reduced to a thumbnail and zeroed at once.
func (s *Service) watch(ctx context.Context, wgen uint64, at int, area contracts.Rect) {
	interval := s.cfg.Watch.Interval
	if interval <= 0 {
		interval = WatchInterval
	}
	req := s.watchRequest(area)
	var (
		d     detector
		thumb Thumb
		fails int
	)
	defer d.close()
	defer thumb.Zero()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		caps, err := s.cfg.Watch.Capturer.Capture(ctx, req)
		if ctx.Err() != nil {
			releaseCaps(caps)
			return
		}
		if err != nil || len(caps) == 0 || caps[0] == nil || caps[0].Image == nil {
			releaseCaps(caps)
			if fails++; fails >= maxWatchErrors {
				s.cfg.Log.Warn("walkthrough watch stopped", "err", err)
				return
			}
			continue
		}
		fails = 0
		thumb.From(caps[0].Image, thumbSide)
		releaseCaps(caps)
		v := d.observe(&thumb, s.cfg.Now())
		thumb.Zero()
		switch v {
		case giveUp:
			return
		case askModel:
			moved, newArea := s.check(ctx, wgen, at)
			if moved || ctx.Err() != nil {
				return
			}
			if newArea != (contracts.Rect{}) {
				// Marked anew: watch around the new hints from a new baseline.
				req = s.watchRequest(newArea)
				d.rebase()
			}
		}
	}
}

// check asks the model whether step at is done and applies its verdict;
// moved reports that the walkthrough went elsewhere.
func (s *Service) check(ctx context.Context, wgen uint64, at int) (moved bool, area contracts.Rect) {
	s.mu.Lock()
	if s.wgen != wgen || s.st.Index != at || s.st.Status != StatusRunning {
		s.mu.Unlock()
		return true, contracts.Rect{}
	}
	// A new generation: hints drawn by the check replace the step's.
	s.gen++
	p := s.presentationLocked()
	s.mu.Unlock()
	v, err := s.cfg.Presenter.Check(ctx, p)
	if ctx.Err() != nil {
		return true, contracts.Rect{}
	}
	if err != nil {
		s.cfg.Log.Warn("walkthrough step check", "err", err)
		return false, contracts.Rect{}
	}
	s.mu.Lock()
	if s.wgen != wgen {
		s.mu.Unlock()
		return true, contracts.Rect{}
	}
	revised := s.reviseLocked(at, v.Step)
	if revised {
		s.publishLocked()
	}
	s.mu.Unlock()
	if revised {
		s.persist()
	}
	if v.Control != nil {
		err := s.Control(ctx, *v.Control, at)
		if err == nil {
			return true, contracts.Rect{}
		}
		if !errors.Is(err, ErrMoved) && !errors.Is(err, ErrNotActive) {
			s.cfg.Log.Warn("walkthrough auto-advance", "err", err)
		}
	}
	return false, v.Area
}

func releaseCaps(caps []*contracts.Capture) {
	for _, c := range caps {
		if c != nil {
			c.Release()
		}
	}
}
