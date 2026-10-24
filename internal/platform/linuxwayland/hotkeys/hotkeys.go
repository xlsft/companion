//go:build linux

// Package hotkeys implements contracts.HotkeyService with the xdg
// GlobalShortcuts portal (GNOME 48+, KDE Plasma, Hyprland).
//
// SupportsRelease starts false and turns true only after a Deactivated
// signal was observed for a shortcut that had been Activated. The spec
// defines Deactivated, but whether a backend sends it on key-up is not
// discoverable up front (OC-6: unverified on GNOME, missing entirely on
// backends that only forward compositor binds). Claiming release support
// wrongly would leave hold-to-talk recording forever, while starting in
// toggle mode only costs a mode switch at the next session once the first
// release proves the backend sends key-ups.
//
// A backend that refuses BindShortcuts (niri via xdg-desktop-portal-gnome,
// OC-6) yields contracts.Unsupported(CapGlobalHotkey) so the chooser falls
// back to the control socket.
package hotkeys

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/godbus/dbus/v5"

	"github.com/xlsft/companion/internal/platform/linuxwayland/portal"
	"github.com/xlsft/companion/pkg/contracts"
)

const desktopPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")

// bus is the part of *portal.Conn the service uses, so tests can fake it.
type bus interface {
	Version(iface string) uint32
	CreateSession(ctx context.Context, iface string, extra map[string]dbus.Variant) (dbus.ObjectPath, error)
	Request(ctx context.Context, method string, options map[string]dbus.Variant, args ...any) (map[string]dbus.Variant, error)
	SubscribeMembers(path dbus.ObjectPath, iface string, members ...string) (<-chan *dbus.Signal, func(), error)
	CloseSession(session dbus.ObjectPath) error
	Close() error
}

// Service is the GlobalShortcuts backend. Create it with Open.
type Service struct {
	bus bus

	mu        sync.Mutex
	session   dbus.ObjectPath
	requested []contracts.HotkeyBinding
	bound     []contracts.HotkeyBinding
	active    map[string]bool // shortcut ids between Activated and Deactivated

	sawRelease atomic.Bool
	events     chan contracts.HotkeyEvent
	stop       chan struct{}
	done       chan struct{}
	cancels    []func()
	closeOnce  sync.Once
}

// Open connects to the session bus. It fails with Unsupported when the
// portal does not export GlobalShortcuts at all.
func Open(ctx context.Context) (*Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := portal.Connect()
	if err != nil {
		return nil, err
	}
	s, err := newService(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	return s, nil
}

func newService(b bus) (*Service, error) {
	if b.Version(portal.IfGlobalShortcuts) == 0 {
		return nil, fmt.Errorf("global shortcuts portal absent: %w", contracts.Unsupported(contracts.CapGlobalHotkey))
	}
	s := &Service{
		bus:    b,
		active: map[string]bool{},
		events: make(chan contracts.HotkeyEvent, 64),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	// One channel for all members keeps Activated/Deactivated in bus order;
	// with separate channels select could see a key-up before its key-down.
	sigs, cancel, err := b.SubscribeMembers(desktopPath, portal.IfGlobalShortcuts, "Activated", "Deactivated", "ShortcutsChanged")
	if err != nil {
		return nil, fmt.Errorf("global shortcuts: subscribe: %w", err)
	}
	s.cancels = append(s.cancels, cancel)
	go s.loop(sigs)
	return s, nil
}

func (s *Service) unsubscribe() {
	for _, c := range s.cancels {
		c()
	}
	s.cancels = nil
}

func (s *Service) loop(sigs <-chan *dbus.Signal) {
	defer close(s.done)
	defer close(s.events)
	for {
		var sig *dbus.Signal
		select {
		case <-s.stop:
			return
		case sig = <-sigs:
		}
		switch sig.Name[strings.LastIndex(sig.Name, ".")+1:] {
		case "Activated":
			if id, ok := s.ours(sig); ok {
				s.mu.Lock()
				s.active[id] = true
				s.mu.Unlock()
				s.emit(contracts.HotkeyEvent{ID: contracts.HotkeyID(id), Kind: contracts.HotkeyPressed})
			}
		case "Deactivated":
			if id, ok := s.ours(sig); ok {
				s.mu.Lock()
				wasActive := s.active[id]
				delete(s.active, id)
				s.mu.Unlock()
				if wasActive {
					s.sawRelease.Store(true)
					s.emit(contracts.HotkeyEvent{ID: contracts.HotkeyID(id), Kind: contracts.HotkeyReleased})
				}
			}
		case "ShortcutsChanged":
			sess, infos, ok := shortcutsChanged(sig.Body)
			s.mu.Lock()
			if ok && sess == s.session && sess != "" {
				s.bound = granted(s.requested, infos)
			}
			s.mu.Unlock()
		}
	}
}

func (s *Service) ours(sig *dbus.Signal) (string, bool) {
	sess, id, ok := activation(sig.Body)
	if !ok {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return id, sess != "" && sess == s.session
}

// emit drops events when the consumer is not reading rather than stalling
// the D-Bus signal dispatch shared with other portal users.
func (s *Service) emit(ev contracts.HotkeyEvent) {
	select {
	case s.events <- ev:
	default:
	}
}

// Register starts a new portal session and binds bindings in it; a session
// can bind only once, so replacing the set means replacing the session. The
// backend may show a dialog where the user confirms or changes the chords,
// so ctx should allow for user interaction.
func (s *Service) Register(ctx context.Context, bindings []contracts.HotkeyBinding) ([]contracts.HotkeyBinding, error) {
	arg, err := shortcutsArg(bindings)
	if err != nil {
		return nil, err
	}
	s.closeSession()
	if len(bindings) == 0 {
		return nil, nil
	}
	sess, err := s.bus.CreateSession(ctx, portal.IfGlobalShortcuts, nil)
	if err != nil {
		return nil, mapErr("CreateSession", err)
	}
	s.mu.Lock()
	s.session = sess
	s.requested = append([]contracts.HotkeyBinding(nil), bindings...)
	s.mu.Unlock()

	res, err := s.bus.Request(ctx, portal.IfGlobalShortcuts+".BindShortcuts", nil, sess, arg, "")
	if err != nil {
		s.closeSession()
		return nil, mapErr("BindShortcuts", err)
	}
	infos := decodeShortcuts(res["shortcuts"])
	if _, ok := res["shortcuts"]; !ok {
		// Some backends answer BindShortcuts without the list; ask for it.
		lres, lerr := s.bus.Request(ctx, portal.IfGlobalShortcuts+".ListShortcuts", nil, sess)
		if lerr != nil {
			s.closeSession()
			return nil, mapErr("ListShortcuts", lerr)
		}
		infos = decodeShortcuts(lres["shortcuts"])
	}
	g := granted(bindings, infos)
	s.mu.Lock()
	s.bound = g
	s.mu.Unlock()
	return append([]contracts.HotkeyBinding(nil), g...), nil
}

// mapErr turns portal outcomes into contract errors. ErrFailed is what a
// backend without a shortcuts provider answers (niri), so it means "use the
// fallback"; a dismissed dialog is the user's refusal.
func mapErr(op string, err error) error {
	switch {
	case errors.Is(err, portal.ErrCancelled):
		return fmt.Errorf("global shortcuts: %s: %w", op, contracts.PermissionDenied(contracts.CapGlobalHotkey))
	case errors.Is(err, portal.ErrFailed):
		return fmt.Errorf("global shortcuts: %s refused by portal backend: %w", op, contracts.Unsupported(contracts.CapGlobalHotkey))
	}
	return fmt.Errorf("global shortcuts: %s: %w", op, err)
}

func (s *Service) closeSession() {
	s.mu.Lock()
	sess := s.session
	s.session = ""
	s.bound = nil
	s.requested = nil
	clear(s.active)
	s.mu.Unlock()
	if sess != "" {
		_ = s.bus.CloseSession(sess)
	}
}

// Bindings returns what the system currently grants, updated by
// ShortcutsChanged when the user edits the chords in system settings.
func (s *Service) Bindings() []contracts.HotkeyBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contracts.HotkeyBinding(nil), s.bound...)
}

// Unregister closes the portal session, which drops its shortcuts.
func (s *Service) Unregister(ctx context.Context) error {
	s.closeSession()
	return ctx.Err()
}

// SupportsRelease reports whether a key-up was ever seen; see package doc.
func (s *Service) SupportsRelease() bool { return s.sawRelease.Load() }

func (s *Service) Events() <-chan contracts.HotkeyEvent { return s.events }

// Close ends the session, closes Events and the bus connection.
func (s *Service) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.closeSession()
		s.unsubscribe()
		close(s.stop)
		<-s.done
		err = s.bus.Close()
	})
	return err
}
