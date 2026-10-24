package appearance

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	tests := []struct {
		name       string
		portal     uint32
		havePortal bool
		gs, gtk    string
		want       bool
	}{
		{"portal dark", 1, true, "'default'", "Adwaita", true},
		{"portal light beats gsettings", 2, true, "'prefer-dark'", "Adwaita-dark", false},
		{"no preference falls to gsettings dark", 0, true, "'prefer-dark'\n", "", true},
		{"no preference falls to gsettings light", 0, true, "'prefer-light'", "Adwaita-dark", false},
		{"no preference default uses gtk theme", 0, true, "'default'", "'Adwaita-dark'", true},
		{"no portal gtk theme dark", 0, false, "", "Materia-DARK", true},
		{"nothing known is light", 0, false, "", "", false},
		{"unknown portal value", 7, true, "", "Adwaita", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decide(tc.portal, tc.havePortal, tc.gs, tc.gtk); got != tc.want {
				t.Errorf("decide = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStateSignalsOnlyOnChange(t *testing.T) {
	s := newState()
	s.set(false)
	select {
	case <-s.Changes():
		t.Fatal("signal without a change")
	default:
	}
	s.set(true)
	s.set(false) // coalesces with the pending signal
	n := 0
	for {
		select {
		case <-s.Changes():
			n++
			continue
		default:
		}
		break
	}
	if n != 1 || s.Dark() {
		t.Errorf("signals = %d dark = %v, want 1 and false", n, s.Dark())
	}
}

func TestPollFollowsReadAndKeepsValueOnError(t *testing.T) {
	s := newState()
	vals := make(chan func() (bool, error), 3)
	vals <- func() (bool, error) { return true, nil }
	vals <- func() (bool, error) { return false, errors.New("boom") }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.poll(ctx, time.Millisecond, func(context.Context) (bool, error) {
			select {
			case f := <-vals:
				return f()
			default:
				return false, errors.New("idle")
			}
		})
	}()
	select {
	case <-s.Changes():
	case <-time.After(2 * time.Second):
		t.Fatal("no change signal")
	}
	time.Sleep(20 * time.Millisecond)
	if !s.Dark() {
		t.Error("a failed read reset the scheme")
	}
	cancel()
	<-done
}

func TestFixed(t *testing.T) {
	f := Fixed(true)
	if err := f.Start(context.Background()); err != nil || !f.Dark() {
		t.Errorf("Fixed(true): err=%v dark=%v", err, f.Dark())
	}
}
