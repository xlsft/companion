package wl

import (
	"bytes"
	"testing"
)

func TestMsgEventRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		s    string
	}{
		{"empty", ""},
		{"three", "abc"},
		{"four", "abcd"},
		{"iface", "zwlr_layer_shell_v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMsg().Uint(7).String(tt.s).Int(-3).Fixed(1.5)
			if len(m.b)%4 != 0 {
				t.Fatalf("payload not 4-aligned: %d", len(m.b))
			}
			ev := &Event{b: m.b}
			if got := ev.Uint(); got != 7 {
				t.Fatalf("uint = %d", got)
			}
			if got := ev.String(); got != tt.s {
				t.Fatalf("string = %q, want %q", got, tt.s)
			}
			if got := ev.Int(); got != -3 {
				t.Fatalf("int = %d", got)
			}
			if got := ev.Fixed(); got != 1.5 {
				t.Fatalf("fixed = %v", got)
			}
		})
	}
}

func TestStringEncoding(t *testing.T) {
	got := NewMsg().String("abc").b
	want := []byte{4, 0, 0, 0, 'a', 'b', 'c', 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEventShortRead(t *testing.T) {
	ev := &Event{b: []byte{1, 0}}
	if ev.Uint() != 0 || ev.String() != "" {
		t.Fatal("short reads must yield zero values")
	}
}
