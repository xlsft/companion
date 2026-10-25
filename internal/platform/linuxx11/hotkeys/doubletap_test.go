package hotkeys

import (
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/xlsft/companion/internal/doubletap"
)

func TestTapClass(t *testing.T) {
	km := testKeymap(t, 4, 6)
	tests := []struct {
		code xproto.Keycode
		want string
	}{{37, "ctrl"}, {50, "shift"}, {64, "alt"}, {133, "super"}, {38, ""}, {66, ""}, {9, ""}}
	for _, tt := range tests {
		if got := km.tapClass(tt.code); got != tt.want {
			t.Errorf("tapClass(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

// held builds a QueryKeymap bitmap with the given keycodes down.
func held(codes ...int) [32]byte {
	var b [32]byte
	for _, c := range codes {
		b[c/8] |= 1 << (c % 8)
	}
	return b
}

func TestTapScan(t *testing.T) {
	const ctrl, a, b = 37, 38, 56
	class := func(code xproto.Keycode) string {
		if code == ctrl {
			return "ctrl"
		}
		return ""
	}
	type reading struct {
		ms   int
		keys [32]byte
	}
	tests := []struct {
		name     string
		readings []reading
		fires    int
	}{
		{"double tap", []reading{{0, held()}, {20, held(ctrl)}, {100, held()}, {200, held(ctrl)}, {260, held()}}, 1},
		{"held at start is no press", []reading{{0, held(ctrl)}, {60, held()}, {200, held(ctrl)}, {260, held()}}, 0},
		{"chord in one period", []reading{{0, held()}, {20, held(ctrl)}, {100, held()}, {200, held(ctrl)}, {220, held(a)}, {260, held()}}, 0},
		{"chord", []reading{{0, held()}, {20, held(ctrl)}, {100, held()}, {200, held(ctrl)}, {220, held(ctrl, a)}, {240, held(ctrl)}, {260, held()}}, 0},
		{"typing between", []reading{{0, held()}, {20, held(ctrl)}, {80, held()}, {120, held(a)}, {140, held()}, {200, held(ctrl)}, {260, held()}}, 0},
		{"one of two other keys released", []reading{{0, held()}, {20, held(a, b)}, {40, held(a)}, {60, held(a, ctrl)}, {80, held(a)}, {100, held()}}, 0},
		{"slow", []reading{{0, held()}, {20, held(ctrl)}, {80, held()}, {600, held(ctrl)}, {660, held()}}, 0},
	}
	t0 := time.Unix(1000, 0)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &tapScan{det: doubletap.New("ctrl")}
			n := 0
			for _, r := range tt.readings {
				if s.feed(r.keys, t0.Add(time.Duration(r.ms)*time.Millisecond), class) {
					n++
				}
			}
			if n != tt.fires {
				t.Errorf("fired %d times, want %d", n, tt.fires)
			}
		})
	}
}
