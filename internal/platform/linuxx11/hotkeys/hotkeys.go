// Package hotkeys implements contracts.HotkeyService on X11 with passive
// key grabs on the root window (pure-Go xgb, no libX11). X delivers both
// KeyPress and KeyRelease for a grabbed key, so hold-to-talk works.
//
// Under Xwayland the grabs only see keys while an X client has focus; on a
// Wayland session the chooser in internal/platform/hotkeys uses the portal
// or the control socket instead.
package hotkeys

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/xlsft/openclick/internal/platform/keys"
	"github.com/xlsft/openclick/pkg/contracts"
)

// repeatWindow is how long a release is held back waiting for the press
// that would mark it as autorepeat. The server writes both events in one
// burst, so a few milliseconds suffice; 20 ms stays far below what a user
// can notice on key-up.
const repeatWindow = 20 * time.Millisecond

type grab struct {
	code xproto.Keycode
	mods uint16 // without lock bits
	id   contracts.HotkeyID
}

// Service is the X11 hotkey backend. Create it with Open.
type Service struct {
	conn *xgb.Conn
	root xproto.Window

	mu       sync.Mutex
	km       keymap
	bindings []contracts.HotkeyBinding
	grabs    []grab
	filter   *repeatFilter

	events    chan contracts.HotkeyEvent
	raw       chan xgb.Event
	done      chan struct{}
	closeOnce sync.Once
}

// Open connects to the X server named by $DISPLAY.
func Open() (*Service, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("x11 hotkeys: connect: %w", err)
	}
	s := &Service{
		conn:   conn,
		root:   xproto.Setup(conn).DefaultScreen(conn).Root,
		filter: newRepeatFilter(),
		events: make(chan contracts.HotkeyEvent, 64),
		raw:    make(chan xgb.Event, 64),
		done:   make(chan struct{}),
	}
	km, err := s.loadKeymap()
	if err != nil {
		conn.Close()
		return nil, err
	}
	s.km = km
	go s.read()
	go s.loop()
	return s, nil
}

func (s *Service) loadKeymap() (keymap, error) {
	setup := xproto.Setup(s.conn)
	count := int(setup.MaxKeycode) - int(setup.MinKeycode) + 1
	km, err := xproto.GetKeyboardMapping(s.conn, setup.MinKeycode, byte(count)).Reply()
	if err != nil {
		return keymap{}, fmt.Errorf("x11 hotkeys: keyboard mapping: %w", err)
	}
	mm, err := xproto.GetModifierMapping(s.conn).Reply()
	if err != nil {
		return keymap{}, fmt.Errorf("x11 hotkeys: modifier mapping: %w", err)
	}
	return newKeymap(setup.MinKeycode, int(km.KeysymsPerKeycode), km.Keysyms,
		int(mm.KeycodesPerModifier), mm.Keycodes), nil
}

// read moves X events to the loop so the loop can also run timers.
func (s *Service) read() {
	defer close(s.raw)
	for {
		ev, err := s.conn.WaitForEvent()
		if ev == nil && err == nil {
			return // connection closed
		}
		if err != nil {
			continue // error for an unchecked request; nothing to report
		}
		s.raw <- ev
	}
}

func (s *Service) loop() {
	defer close(s.done)
	defer close(s.events)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case ev, ok := <-s.raw:
			if !ok {
				return
			}
			s.handle(ev, timer)
		case <-timer.C:
			s.mu.Lock()
			out := s.filter.expire()
			s.mu.Unlock()
			s.emit(out)
		}
	}
}

func (s *Service) handle(ev xgb.Event, timer *time.Timer) {
	var out []contracts.HotkeyEvent
	s.mu.Lock()
	switch e := ev.(type) {
	case xproto.KeyPressEvent:
		out = s.filter.press(byte(e.Detail), uint32(e.Time), s.match(e.Detail, e.State))
	case xproto.KeyReleaseEvent:
		s.filter.release(byte(e.Detail), uint32(e.Time))
		timer.Reset(repeatWindow)
	case xproto.MappingNotifyEvent:
		if e.Request == xproto.MappingKeyboard || e.Request == xproto.MappingModifier {
			// Keycodes and modifier roles may have moved: grab again.
			// Servers also send this whenever input switches between
			// devices with different keymaps (XTEST, a second keyboard),
			// so regrab only on a real change.
			if km, err := s.loadKeymap(); err == nil && !km.equal(s.km) {
				s.km = km
				_, _ = s.apply(s.bindings)
			}
		}
	}
	s.mu.Unlock()
	s.emit(out)
}

func (s *Service) match(code xproto.Keycode, state uint16) contracts.HotkeyID {
	mods := state & s.km.relevant
	for _, g := range s.grabs {
		if g.code == code && g.mods == mods {
			return g.id
		}
	}
	return ""
}

// emit never blocks the X event loop: a consumer that stops reading loses
// hotkey events rather than stalling the connection.
func (s *Service) emit(evs []contracts.HotkeyEvent) {
	for _, ev := range evs {
		select {
		case s.events <- ev:
		default:
		}
	}
}

// Register grabs every binding with all lock-modifier combinations. A chord
// held by another client fails with contracts.ErrHotkeyConflict naming
// the binding; the other bindings are still registered and returned.
func (s *Service) Register(ctx context.Context, bindings []contracts.HotkeyBinding) ([]contracts.HotkeyBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apply(bindings)
}

// apply must be called with mu held.
func (s *Service) apply(bindings []contracts.HotkeyBinding) ([]contracts.HotkeyBinding, error) {
	s.ungrabAll()
	s.filter.reset()
	s.bindings = append([]contracts.HotkeyBinding(nil), bindings...)
	var granted []contracts.HotkeyBinding
	var errs []error
	for _, b := range bindings {
		gs, err := s.grabBinding(b)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s.grabs = append(s.grabs, gs...)
		granted = append(granted, b)
	}
	return granted, errors.Join(errs...)
}

func (s *Service) grabBinding(b contracts.HotkeyBinding) ([]grab, error) {
	chord := keys.String(b.Hotkey)
	sym, ok := keys.Keysym(b.Hotkey.Key)
	if !ok {
		return nil, fmt.Errorf("hotkey %s (%s): unknown key", b.ID, chord)
	}
	codes := s.km.keycodes(sym)
	if len(codes) == 0 {
		return nil, fmt.Errorf("hotkey %s (%s): key not on the current keyboard layout: %w",
			b.ID, chord, contracts.Unsupported(contracts.CapGlobalHotkey))
	}
	mods := s.km.mask(b.Hotkey.Mods)
	var done []grab
	for _, code := range codes {
		for _, lock := range s.km.locks {
			err := xproto.GrabKeyChecked(s.conn, false, s.root, mods|lock, code,
				xproto.GrabModeAsync, xproto.GrabModeAsync).Check()
			if err != nil {
				for _, g := range done {
					s.ungrab(g)
				}
				if _, ok := err.(xproto.AccessError); ok {
					return nil, fmt.Errorf("hotkey %s (%s): %w", b.ID, chord, contracts.ErrHotkeyConflict)
				}
				return nil, fmt.Errorf("hotkey %s (%s): grab failed: %w", b.ID, chord, err)
			}
		}
		done = append(done, grab{code: code, mods: mods, id: b.ID})
	}
	return done, nil
}

func (s *Service) ungrab(g grab) {
	for _, lock := range s.km.locks {
		_ = xproto.UngrabKeyChecked(s.conn, g.code, s.root, g.mods|lock).Check()
	}
}

func (s *Service) ungrabAll() {
	for _, g := range s.grabs {
		s.ungrab(g)
	}
	s.grabs = nil
}

// Unregister releases every grab.
func (s *Service) Unregister(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ungrabAll()
	s.filter.reset()
	s.bindings = nil
	return ctx.Err()
}

// SupportsRelease is always true: X sends KeyRelease for grabbed keys.
func (s *Service) SupportsRelease() bool { return true }

func (s *Service) Events() <-chan contracts.HotkeyEvent { return s.events }

// Close drops the connection, which releases all grabs, and closes Events.
func (s *Service) Close() error {
	s.closeOnce.Do(s.conn.Close)
	<-s.done
	return nil
}
