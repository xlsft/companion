package hotkeys

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

type fakeBackend struct {
	name     string
	regErr   error
	release  bool
	events   chan contracts.HotkeyEvent
	mu       sync.Mutex
	regs     int
	closed   bool
	closeOne sync.Once
}

func newFake(name string) *fakeBackend {
	return &fakeBackend{name: name, events: make(chan contracts.HotkeyEvent, 4)}
}

func (f *fakeBackend) Register(_ context.Context, b []contracts.HotkeyBinding) ([]contracts.HotkeyBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.regs++
	if f.regErr != nil {
		return nil, f.regErr
	}
	return b, nil
}
func (f *fakeBackend) Unregister(context.Context) error     { return nil }
func (f *fakeBackend) SupportsRelease() bool                { return f.release }
func (f *fakeBackend) Events() <-chan contracts.HotkeyEvent { return f.events }
func (f *fakeBackend) Close() error {
	f.closeOne.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		close(f.events)
	})
	return nil
}

var errBoom = errors.New("boom")

func TestChoose(t *testing.T) {
	unsupported := contracts.Unsupported(contracts.CapGlobalHotkey)
	conflict := errors.Join(errors.New("hotkey talk"), contracts.ErrHotkeyConflict)
	tests := []struct {
		name      string
		session   contracts.SessionKind
		facts     map[string]string
		x11Err    error
		portalErr error
		ipcErr    error
		regErr    error // primary Register
		wantNew   error
		wantReg   error
		before    string // Backend() after New
		after     string // Backend() after Register
		ipcRegs   int
	}{
		{name: "x11", session: contracts.SessionX11, before: BackendX11, after: BackendX11, ipcRegs: 1},
		{name: "x11 conflict keeps x11 and ipc", session: contracts.SessionX11, regErr: conflict, wantReg: contracts.ErrHotkeyConflict, before: BackendX11, after: BackendX11, ipcRegs: 1},
		{name: "x11 connect fails", session: contracts.SessionX11, x11Err: errBoom, before: BackendIPC, after: BackendIPC, ipcRegs: 1},
		{name: "wayland portal works", session: contracts.SessionWayland, before: BackendPortal, after: BackendPortal, ipcRegs: 1},
		{name: "niri: BindShortcuts unsupported", session: contracts.SessionWayland, regErr: unsupported, before: BackendPortal, after: BackendIPC, ipcRegs: 1},
		{name: "portal dialog dismissed", session: contracts.SessionWayland, regErr: contracts.PermissionDenied(contracts.CapGlobalHotkey), wantReg: contracts.ErrPermissionDenied, before: BackendPortal, after: BackendPortal, ipcRegs: 1},
		{name: "portal absent per report", session: contracts.SessionWayland, facts: map[string]string{"portal.GlobalShortcuts.version": "absent"}, before: BackendIPC, after: BackendIPC, ipcRegs: 1},
		{name: "portal open unsupported", session: contracts.SessionWayland, portalErr: unsupported, before: BackendIPC, after: BackendIPC, ipcRegs: 1},
		{name: "no session: ipc only", session: "", before: BackendIPC, after: BackendIPC, ipcRegs: 1},
		{name: "ipc fails, x11 alone", session: contracts.SessionX11, ipcErr: errBoom, before: BackendX11, after: BackendX11},
		{name: "ipc fails, portal unsupported at register", session: contracts.SessionWayland, ipcErr: errBoom, regErr: unsupported, wantReg: contracts.ErrUnsupported, before: BackendPortal, after: BackendPortal},
		{name: "nothing at all", session: contracts.SessionWayland, portalErr: unsupported, ipcErr: errBoom, wantNew: contracts.ErrUnsupported},
	}
	bindings := []contracts.HotkeyBinding{{ID: "talk"}, {ID: "cancel"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x11, portal, ipc := newFake(BackendX11), newFake(BackendPortal), newFake(BackendIPC)
			x11.regErr, portal.regErr = tt.regErr, tt.regErr
			d := deps{
				// Mirror the real constructors: typed nil pointer on error.
				x11: func() (backend, error) {
					if tt.x11Err != nil {
						return (*fakeBackend)(nil), tt.x11Err
					}
					return x11, nil
				},
				portal: func(context.Context) (backend, error) {
					if tt.portalErr != nil {
						return (*fakeBackend)(nil), tt.portalErr
					}
					return portal, nil
				},
				ipc: func() (backend, error) {
					if tt.ipcErr != nil {
						return (*fakeBackend)(nil), tt.ipcErr
					}
					return ipc, nil
				},
			}
			s, err := choose(context.Background(), contracts.CapabilityReport{Session: tt.session, Facts: tt.facts}, d)
			if tt.wantNew != nil {
				if !errors.Is(err, tt.wantNew) {
					t.Fatalf("choose err = %v, want %v", err, tt.wantNew)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if got := s.Backend(); got != tt.before {
				t.Errorf("Backend() = %q, want %q", got, tt.before)
			}
			_, err = s.Register(context.Background(), bindings)
			if (tt.wantReg == nil) != (err == nil) || (tt.wantReg != nil && !errors.Is(err, tt.wantReg)) {
				t.Errorf("Register err = %v, want %v", err, tt.wantReg)
			}
			if got := s.Backend(); got != tt.after {
				t.Errorf("Backend() after Register = %q, want %q", got, tt.after)
			}
			if ipc.regs != tt.ipcRegs {
				t.Errorf("ipc registered %d times, want %d", ipc.regs, tt.ipcRegs)
			}
			if tt.before == BackendPortal && tt.after == BackendIPC && !portal.closed {
				t.Error("unsupported portal not closed")
			}
		})
	}
}

func TestServiceMergesEventsAndRelease(t *testing.T) {
	primary, ipc := newFake(BackendPortal), newFake(BackendIPC)
	s := newService(primary, BackendPortal, ipc)
	if s.SupportsRelease() {
		t.Fatal("release claimed")
	}
	ipc.release = true // e.g. a Hyprland bindr release arrived
	if !s.SupportsRelease() {
		t.Fatal("ipc release not reported")
	}
	primary.events <- contracts.HotkeyEvent{ID: "talk", Kind: contracts.HotkeyPressed}
	ipc.events <- contracts.HotkeyEvent{ID: "talk", Kind: contracts.HotkeyToggled}
	got := map[contracts.HotkeyEventKind]bool{}
	for range 2 {
		select {
		case ev := <-s.Events():
			got[ev.Kind] = true
		case <-time.After(2 * time.Second):
			t.Fatal("missing event")
		}
	}
	if !got[contracts.HotkeyPressed] || !got[contracts.HotkeyToggled] {
		t.Errorf("events %v", got)
	}
	s.Close()
	select {
	case _, ok := <-s.Events():
		if ok {
			t.Error("unexpected event after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Events not closed")
	}
}

// tapBackend is a fakeBackend that can watch for a double tap.
type tapBackend struct {
	*fakeBackend
	key string
}

func (b *tapBackend) SetDoubleTap(_ context.Context, _ contracts.HotkeyID, key string) error {
	b.key = key
	return nil
}

func TestSetDoubleTap(t *testing.T) {
	tests := []struct {
		name       string
		primary    string // "", "plain" (portal), "tap" (x11)
		ipc        bool
		key        string
		wantErr    error
		wantDirect bool
		primaryKey string
		ipcKey     string
	}{
		{name: "x11 sees it", primary: "tap", ipc: true, key: "ctrl", wantDirect: true, primaryKey: "ctrl"},
		{name: "portal: socket counts compositor taps", primary: "plain", ipc: true, key: "super", ipcKey: "super"},
		{name: "socket only", ipc: true, key: "alt", ipcKey: "alt"},
		{name: "portal without socket", primary: "plain", key: "ctrl", wantErr: contracts.ErrUnsupported},
		{name: "off without any", primary: "plain", key: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var primary, ipc backend
			var ptap, itap *tapBackend
			switch tt.primary {
			case "plain":
				primary = newFake(BackendPortal)
			case "tap":
				ptap = &tapBackend{fakeBackend: newFake(BackendX11)}
				primary = ptap
			}
			if tt.ipc {
				itap = &tapBackend{fakeBackend: newFake(BackendIPC), key: "stale"}
				ipc = itap
			}
			s := newService(primary, BackendX11, ipc)
			defer s.Close()
			err := s.SetDoubleTap(context.Background(), "talk", tt.key)
			if (tt.wantErr == nil) != (err == nil) || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := s.DoubleTapDirect(); got != tt.wantDirect {
				t.Errorf("DoubleTapDirect = %v", got)
			}
			if ptap != nil && ptap.key != tt.primaryKey {
				t.Errorf("primary key %q, want %q", ptap.key, tt.primaryKey)
			}
			if itap != nil && itap.key != tt.ipcKey {
				t.Errorf("socket key %q, want %q", itap.key, tt.ipcKey)
			}
		})
	}
}
