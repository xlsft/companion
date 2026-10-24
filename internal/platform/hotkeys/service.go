// Package hotkeys picks the global-hotkey backend for the running session
// and combines it with the control socket, which always runs so that
// "companion ctl" works everywhere:
//
//	X11 session      -> X11 key grabs           + control socket
//	Wayland, portal  -> GlobalShortcuts portal  + control socket
//	Wayland, other   -> control socket only (compositor binding)
//
// A portal whose backend refuses BindShortcuts (niri) is dropped at the
// first Register and the service continues on the socket alone.
package hotkeys

import (
	"context"
	"errors"
	"sync"

	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/pkg/contracts"
)

// Backend names, as the capability report uses them.
const (
	BackendX11    = detect.BackendX11Grab
	BackendPortal = detect.BackendPortal
	BackendIPC    = detect.BackendIPC
)

// backend is a HotkeyService that can be stopped.
type backend interface {
	contracts.HotkeyService
	Close() error
}

// Service implements contracts.HotkeyService over a primary backend (may be
// absent) and the control socket (may be absent if it could not listen).
type Service struct {
	mu          sync.Mutex
	primary     backend
	primaryName string
	ipc         backend

	events    chan contracts.HotkeyEvent
	fwd       sync.WaitGroup
	closeOnce sync.Once
}

func newService(primary backend, primaryName string, ipc backend) *Service {
	s := &Service{primary: primary, primaryName: primaryName, ipc: ipc, events: make(chan contracts.HotkeyEvent, 64)}
	for _, b := range []backend{primary, ipc} {
		if b == nil {
			continue
		}
		s.fwd.Add(1)
		go func() {
			defer s.fwd.Done()
			for ev := range b.Events() {
				select {
				case s.events <- ev:
				default: // like the backends: drop rather than stall
				}
			}
		}()
	}
	go func() {
		s.fwd.Wait()
		close(s.events)
	}()
	return s
}

// Backend names the mechanism delivering hotkeys. BackendIPC means the user
// has to bind compositor keys to "companion ctl" (control.Instructions).
func (s *Service) Backend() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.primary != nil {
		return s.primaryName
	}
	return BackendIPC
}

// Register registers bindings with the control socket and the primary
// backend. The returned bindings are what the primary granted, or all of
// them when only the socket is left. If the primary turns out unsupported
// it is closed and nil is returned with the socket bindings; other primary
// errors (conflict, denied consent) are returned as is while the socket
// keeps the bindings usable.
func (s *Service) Register(ctx context.Context, bindings []contracts.HotkeyBinding) ([]contracts.HotkeyBinding, error) {
	var ipcGranted []contracts.HotkeyBinding
	var ipcErr error
	if s.ipc != nil {
		ipcGranted, ipcErr = s.ipc.Register(ctx, bindings)
	}
	s.mu.Lock()
	primary := s.primary
	s.mu.Unlock()
	if primary == nil {
		if s.ipc == nil {
			return nil, contracts.Unsupported(contracts.CapGlobalHotkey)
		}
		return ipcGranted, ipcErr
	}
	granted, err := primary.Register(ctx, bindings)
	if errors.Is(err, contracts.ErrUnsupported) && s.ipc != nil {
		s.mu.Lock()
		if s.primary == primary {
			s.primary = nil
		}
		s.mu.Unlock()
		_ = primary.Close()
		return ipcGranted, ipcErr
	}
	return granted, err
}

func (s *Service) Unregister(ctx context.Context) error {
	var errs []error
	s.mu.Lock()
	primary := s.primary
	s.mu.Unlock()
	if primary != nil {
		errs = append(errs, primary.Unregister(ctx))
	}
	if s.ipc != nil {
		errs = append(errs, s.ipc.Unregister(ctx))
	}
	return errors.Join(errs...)
}

// SupportsRelease is true if any active source has delivered or can
// deliver key-up events.
func (s *Service) SupportsRelease() bool {
	s.mu.Lock()
	primary := s.primary
	s.mu.Unlock()
	return (primary != nil && primary.SupportsRelease()) || (s.ipc != nil && s.ipc.SupportsRelease())
}

func (s *Service) Events() <-chan contracts.HotkeyEvent { return s.events }

// Close stops all backends; Events is closed once they have closed theirs.
func (s *Service) Close() error {
	var errs []error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		primary := s.primary
		s.primary = nil
		s.mu.Unlock()
		if primary != nil {
			errs = append(errs, primary.Close())
		}
		if s.ipc != nil {
			errs = append(errs, s.ipc.Close())
		}
	})
	return errors.Join(errs...)
}
