package hotkeys

import (
	"slices"
	"testing"

	"github.com/jezek/xgb/xproto"

	"github.com/xlsft/openclick/pkg/contracts"
)

// testKeymap builds a small server mapping: keycode -> keysyms (2 per code),
// plus a modifier map with 1 keycode per modifier row.
func testKeymap(t *testing.T, numLockRow, superRow int) keymap {
	t.Helper()
	const minCode = 8
	syms := map[xproto.Keycode][2]xproto.Keysym{
		9:   {0xff1b, 0},      // Escape
		38:  {0x61, 0x41},     // a A
		49:  {0x60, 0x7e},     // grave asciitilde
		65:  {0x20, 0},        // space
		67:  {0xffbe, 0},      // F1
		37:  {0xffe3, 0},      // Control_L
		50:  {0xffe1, 0},      // Shift_L
		64:  {0xffe9, 0xffe7}, // Alt_L Meta_L
		66:  {0xffe5, 0},      // Caps_Lock
		77:  {0xff7f, 0},      // Num_Lock
		133: {0xffeb, 0},      // Super_L
		200: {0x7e, 0},        // asciitilde on its own key, column 0
		201: {0, 0x40},        // only shifted: at
	}
	const maxCode = 255
	flat := make([]xproto.Keysym, (maxCode-minCode+1)*2)
	for kc, s := range syms {
		i := int(kc-minCode) * 2
		flat[i], flat[i+1] = s[0], s[1]
	}
	mod := make([]xproto.Keycode, 8)
	mod[0], mod[1], mod[2], mod[3] = 50, 66, 37, 64
	if numLockRow >= 0 {
		mod[numLockRow] = 77
	}
	if superRow >= 0 {
		mod[superRow] = 133
	}
	return newKeymap(minCode, 2, flat, 1, mod)
}

func TestKeymap(t *testing.T) {
	km := testKeymap(t, 4, 6) // NumLock on Mod2, Super on Mod4: the usual layout
	if km.alt != xproto.ModMask1 || km.super != xproto.ModMask4 {
		t.Fatalf("alt=%#x super=%#x", km.alt, km.super)
	}
	wantLocks := []uint16{0, xproto.ModMaskLock, xproto.ModMask2, xproto.ModMaskLock | xproto.ModMask2}
	if !slices.Equal(km.locks, wantLocks) {
		t.Errorf("locks = %v, want %v", km.locks, wantLocks)
	}
	tests := []struct {
		sym  uint32
		want []xproto.Keycode
	}{
		{0x20, []xproto.Keycode{65}},
		{0xff1b, []xproto.Keycode{9}},
		{0x61, []xproto.Keycode{38}},
		{0x60, []xproto.Keycode{49}},
		{0x7e, []xproto.Keycode{200}}, // unshifted key wins over shifted grave
		{0x40, []xproto.Keycode{201}}, // shifted only: still found
		{0xffbe, []xproto.Keycode{67}},
		{0xffc9, nil}, // F12 not on this keyboard
	}
	for _, tt := range tests {
		if got := km.keycodes(tt.sym); !slices.Equal(got, tt.want) {
			t.Errorf("keycodes(%#x) = %v, want %v", tt.sym, got, tt.want)
		}
	}
	if m := km.mask(contracts.ModCtrl | contracts.ModAlt | contracts.ModShift | contracts.ModSuper); m != 0x4|0x8|0x1|0x40 {
		t.Errorf("mask = %#x", m)
	}
}

func TestKeymapUnusualLayout(t *testing.T) {
	// Super on Mod3 and no NumLock: defaults must not leak in.
	km := testKeymap(t, -1, 5)
	if km.super != xproto.ModMask3 {
		t.Errorf("super = %#x, want Mod3", km.super)
	}
	if !slices.Equal(km.locks, []uint16{0, xproto.ModMaskLock}) {
		t.Errorf("locks = %v", km.locks)
	}
	// NumLock sharing Super's bit must not become a lock combination.
	km = testKeymap(t, 6, 6)
	if slices.Contains(km.locks, xproto.ModMask4) {
		t.Errorf("locks %v include the Super bit", km.locks)
	}
}

func TestLockCombos(t *testing.T) {
	got := lockCombos([]uint16{2, 16, 128})
	if len(got) != 8 {
		t.Fatalf("got %d combos: %v", len(got), got)
	}
	if !slices.Equal(lockCombos(nil), []uint16{0}) {
		t.Error("no locks must still grab the bare chord")
	}
}

type rawEv struct {
	press bool
	code  byte
	ts    uint32
	id    contracts.HotkeyID // match result for presses
}

func TestRepeatFilter(t *testing.T) {
	P := contracts.HotkeyPressed
	R := contracts.HotkeyReleased
	ev := func(id contracts.HotkeyID, k contracts.HotkeyEventKind) contracts.HotkeyEvent {
		return contracts.HotkeyEvent{ID: id, Kind: k}
	}
	expire := rawEv{code: 0xff}
	tests := []struct {
		name string
		in   []rawEv
		want []contracts.HotkeyEvent
	}{
		{
			name: "single stroke",
			in:   []rawEv{{true, 65, 100, "talk"}, {false, 65, 300, ""}, expire},
			want: []contracts.HotkeyEvent{ev("talk", P), ev("talk", R)},
		},
		{
			name: "classic autorepeat pairs share a timestamp",
			in: []rawEv{
				{true, 65, 100, "talk"},
				{false, 65, 600, ""}, {true, 65, 600, "talk"},
				{false, 65, 633, ""}, {true, 65, 633, "talk"},
				{false, 65, 900, ""}, expire,
			},
			want: []contracts.HotkeyEvent{ev("talk", P), ev("talk", R)},
		},
		{
			name: "detectable autorepeat sends presses only",
			in:   []rawEv{{true, 65, 100, "talk"}, {true, 65, 600, "talk"}, {true, 65, 633, "talk"}, {false, 65, 700, ""}, expire},
			want: []contracts.HotkeyEvent{ev("talk", P), ev("talk", R)},
		},
		{
			name: "fast re-press before expire is a new stroke",
			in:   []rawEv{{true, 65, 100, "talk"}, {false, 65, 150, ""}, {true, 65, 160, "talk"}, {false, 65, 200, ""}, expire},
			want: []contracts.HotkeyEvent{ev("talk", P), ev("talk", R), ev("talk", P), ev("talk", R)},
		},
		{
			name: "unmatched keys are ignored",
			in:   []rawEv{{true, 38, 100, ""}, {false, 38, 120, ""}, expire},
			want: nil,
		},
		{
			name: "two bindings overlap",
			in:   []rawEv{{true, 65, 100, "talk"}, {true, 9, 150, "cancel"}, {false, 9, 170, ""}, {false, 65, 180, ""}, expire},
			want: []contracts.HotkeyEvent{ev("talk", P), ev("cancel", P), ev("cancel", R), ev("talk", R)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRepeatFilter()
			var got []contracts.HotkeyEvent
			for _, e := range tt.in {
				switch {
				case e == expire:
					// Several pending releases come out in map order.
					out := f.expire()
					slices.SortFunc(out, func(a, b contracts.HotkeyEvent) int {
						return slices.Index([]contracts.HotkeyID{"cancel", "talk"}, a.ID) - slices.Index([]contracts.HotkeyID{"cancel", "talk"}, b.ID)
					})
					got = append(got, out...)
				case e.press:
					got = append(got, f.press(e.code, e.ts, e.id)...)
				default:
					f.release(e.code, e.ts)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			if f.hasPending() {
				t.Error("pending releases left after expire")
			}
		})
	}
}
