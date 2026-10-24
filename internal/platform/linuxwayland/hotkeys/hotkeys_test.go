//go:build linux

package hotkeys

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/xlsft/companion/internal/platform/linuxwayland/portal"
	"github.com/xlsft/companion/pkg/contracts"
)

// fakeBus plays the portal frontend: it answers requests from a table and
// lets the test fire signals.
type fakeBus struct {
	version uint32
	bindErr error
	bindRes map[string]dbus.Variant
	listRes map[string]dbus.Variant

	mu      sync.Mutex
	sigs    chan *dbus.Signal
	calls   []string
	closed  []dbus.ObjectPath
	nextSes int
}

func newFakeBus() *fakeBus {
	return &fakeBus{version: 2}
}

func (f *fakeBus) Version(string) uint32 { return f.version }

func (f *fakeBus) CreateSession(context.Context, string, map[string]dbus.Variant) (dbus.ObjectPath, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSes++
	f.calls = append(f.calls, "CreateSession")
	return dbus.ObjectPath("/session/" + string(rune('0'+f.nextSes))), nil
}

func (f *fakeBus) Request(_ context.Context, method string, _ map[string]dbus.Variant, args ...any) (map[string]dbus.Variant, error) {
	f.mu.Lock()
	f.calls = append(f.calls, method[strings.LastIndex(method, ".")+1:])
	f.mu.Unlock()
	switch {
	case strings.HasSuffix(method, ".BindShortcuts"):
		if _, ok := args[1].([]shortcut); !ok {
			return nil, errors.New("bad shortcuts arg")
		}
		return f.bindRes, f.bindErr
	case strings.HasSuffix(method, ".ListShortcuts"):
		return f.listRes, nil
	}
	return nil, errors.New("unexpected method " + method)
}

func (f *fakeBus) SubscribeMembers(dbus.ObjectPath, string, ...string) (<-chan *dbus.Signal, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sigs = make(chan *dbus.Signal, 16)
	return f.sigs, func() {}, nil
}

func (f *fakeBus) CloseSession(s dbus.ObjectPath) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, s)
	return nil
}

func (f *fakeBus) Close() error { return nil }

func (f *fakeBus) fire(member string, body ...any) {
	f.mu.Lock()
	ch := f.sigs
	f.mu.Unlock()
	ch <- &dbus.Signal{Name: portal.IfGlobalShortcuts + "." + member, Body: body}
}

func shortcutsRes(kv ...string) map[string]dbus.Variant {
	var arr [][]any
	for i := 0; i+1 < len(kv); i += 2 {
		arr = append(arr, []any{kv[i], opts("trigger_description", kv[i+1])})
	}
	return map[string]dbus.Variant{"shortcuts": dbus.MakeVariant(arr)}
}

func TestRegisterErrors(t *testing.T) {
	tests := []struct {
		name    string
		version uint32
		bindErr error
		want    error
	}{
		{"portal absent", 0, nil, contracts.ErrUnsupported},
		{"backend refuses (niri)", 2, portal.ErrFailed, contracts.ErrUnsupported},
		{"user dismissed dialog", 2, portal.ErrCancelled, contracts.ErrPermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newFakeBus()
			b.version, b.bindErr = tt.version, tt.bindErr
			s, err := newService(b)
			if err == nil {
				defer s.Close()
				_, err = s.Register(context.Background(), []contracts.HotkeyBinding{talk})
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			var ce *contracts.CapabilityError
			if !errors.As(err, &ce) || ce.Capability != contracts.CapGlobalHotkey {
				t.Errorf("err %v does not name %s", err, contracts.CapGlobalHotkey)
			}
			if tt.version != 0 && len(b.closed) != 1 {
				t.Errorf("session not closed after failure: %v", b.closed)
			}
		})
	}
}

func next(t *testing.T, s *Service) contracts.HotkeyEvent {
	t.Helper()
	select {
	case ev := <-s.Events():
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	return contracts.HotkeyEvent{}
}

func TestRegisterAndSignals(t *testing.T) {
	b := newFakeBus()
	b.bindRes = shortcutsRes("talk", "Ctrl+Alt+Space")
	s, err := newService(b)
	if err != nil {
		t.Fatal(err)
	}
	cancel := contracts.HotkeyBinding{ID: "cancel", Hotkey: contracts.Hotkey{Key: "escape"}}
	got, err := s.Register(context.Background(), []contracts.HotkeyBinding{talk, cancel})
	if err != nil {
		t.Fatal(err)
	}
	// The system granted only talk.
	if len(got) != 1 || got[0].ID != "talk" || got[0].TriggerDescription != "Ctrl+Alt+Space" {
		t.Fatalf("granted %+v", got)
	}
	sess := dbus.ObjectPath("/session/1")
	if s.SupportsRelease() {
		t.Fatal("release claimed before any Deactivated")
	}
	b.fire("Activated", dbus.ObjectPath("/session/other"), "talk", uint64(1), map[string]dbus.Variant{})
	b.fire("Deactivated", sess, "talk", uint64(2), map[string]dbus.Variant{}) // without a press: ignored
	b.fire("Activated", sess, "talk", uint64(3), map[string]dbus.Variant{})
	if ev := next(t, s); ev != (contracts.HotkeyEvent{ID: "talk", Kind: contracts.HotkeyPressed}) {
		t.Fatalf("event %+v", ev)
	}
	if s.SupportsRelease() {
		t.Fatal("release claimed after a press only")
	}
	b.fire("Deactivated", sess, "talk", uint64(4), map[string]dbus.Variant{})
	if ev := next(t, s); ev != (contracts.HotkeyEvent{ID: "talk", Kind: contracts.HotkeyReleased}) {
		t.Fatalf("event %+v", ev)
	}
	if !s.SupportsRelease() {
		t.Fatal("release not reported after Deactivated")
	}

	b.fire("ShortcutsChanged", sess, [][]any{{"talk", opts("trigger_description", "Super+F5")}})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if bs := s.Bindings(); len(bs) == 1 && bs[0].Hotkey == (contracts.Hotkey{Mods: contracts.ModSuper, Key: "f5"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ShortcutsChanged not applied: %+v", s.Bindings())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Re-registering replaces the session.
	if _, err := s.Register(context.Background(), []contracts.HotkeyBinding{talk}); err != nil {
		t.Fatal(err)
	}
	if len(b.closed) != 1 || b.closed[0] != sess {
		t.Errorf("closed sessions %v", b.closed)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-s.Events(); ok {
		t.Error("Events not closed")
	}
}

func TestRegisterListsWhenBindOmitsShortcuts(t *testing.T) {
	b := newFakeBus()
	b.bindRes = map[string]dbus.Variant{}
	b.listRes = shortcutsRes("talk", "")
	s, err := newService(b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Register(context.Background(), []contracts.HotkeyBinding{talk})
	if err != nil || len(got) != 1 || got[0].Hotkey != talk.Hotkey {
		t.Fatalf("got %+v, %v", got, err)
	}
	if want := "CreateSession,BindShortcuts,ListShortcuts"; strings.Join(b.calls, ",") != want {
		t.Errorf("calls %v, want %s", b.calls, want)
	}
}
