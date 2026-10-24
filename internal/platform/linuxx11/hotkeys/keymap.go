package hotkeys

import (
	"slices"

	"github.com/jezek/xgb/xproto"

	"github.com/xlsft/companion/pkg/contracts"
)

// Keysyms that identify modifier roles in the server's modifier mapping.
const (
	symNumLock    = 0xff7f
	symScrollLock = 0xff14
	symAltL       = 0xffe9
	symAltR       = 0xffea
	symMetaL      = 0xffe7
	symMetaR      = 0xffe8
	symSuperL     = 0xffeb
	symSuperR     = 0xffec
)

// keymap is a snapshot of the server's keyboard and modifier mappings.
type keymap struct {
	min     xproto.Keycode
	per     int // keysyms per keycode
	keysyms []xproto.Keysym

	alt, super  uint16 // masks for Mod1..Mod5 roles, resolved per server
	locks       []uint16
	relevant    uint16 // modifier bits that take part in matching
	shift, ctrl uint16
}

// newKeymap resolves modifier roles from the modifier mapping (8 rows of
// perMod keycodes: Shift, Lock, Control, Mod1..Mod5). Alt and Super are
// conventionally Mod1 and Mod4, but layouts move them, and NumLock sits on
// whichever ModN the layout chose, so all of them are looked up.
func newKeymap(minCode xproto.Keycode, per int, keysyms []xproto.Keysym, perMod int, modCodes []xproto.Keycode) keymap {
	km := keymap{min: minCode, per: per, keysyms: keysyms, shift: xproto.ModMaskShift, ctrl: xproto.ModMaskControl}
	var num, scroll uint16
	for row := 0; row < 8 && perMod > 0 && row*perMod < len(modCodes); row++ {
		mask := uint16(1) << row
		for _, kc := range modCodes[row*perMod : min((row+1)*perMod, len(modCodes))] {
			if kc == 0 {
				continue
			}
			for _, s := range km.symsOf(kc) {
				switch s {
				case symNumLock:
					num |= mask
				case symScrollLock:
					scroll |= mask
				case symAltL, symAltR, symMetaL, symMetaR:
					if row >= 3 {
						km.alt |= mask
					}
				case symSuperL, symSuperR:
					if row >= 3 {
						km.super |= mask
					}
				}
			}
		}
	}
	if km.alt == 0 {
		km.alt = xproto.ModMask1
	}
	if km.super == 0 {
		km.super = xproto.ModMask4
	}
	km.relevant = km.shift | km.ctrl | km.alt | km.super
	// A lock that shares a bit with a real modifier would turn every grab
	// into "with or without that modifier"; leave it out instead.
	var lockBits []uint16
	for _, l := range []uint16{xproto.ModMaskLock, num, scroll} {
		if l != 0 && l&km.relevant == 0 {
			lockBits = append(lockBits, l)
		}
	}
	km.locks = lockCombos(lockBits)
	return km
}

// lockCombos returns every combination of the lock bits, including none, so
// a grab fires with CapsLock or NumLock on.
func lockCombos(bits []uint16) []uint16 {
	out := []uint16{0}
	for _, b := range bits {
		for _, c := range out {
			if c&b == 0 {
				out = append(out, c|b)
			}
		}
	}
	return dedup(out)
}

func dedup(in []uint16) []uint16 {
	seen := map[uint16]bool{}
	out := in[:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func (km keymap) equal(o keymap) bool {
	return km.min == o.min && km.per == o.per && km.alt == o.alt && km.super == o.super &&
		slices.Equal(km.keysyms, o.keysyms) && slices.Equal(km.locks, o.locks)
}

func (km keymap) symsOf(kc xproto.Keycode) []xproto.Keysym {
	if kc < km.min || km.per == 0 {
		return nil
	}
	i := int(kc-km.min) * km.per
	if i+km.per > len(km.keysyms) {
		return nil
	}
	return km.keysyms[i : i+km.per]
}

// keycodes returns the keycodes that produce sym without modifiers (first
// column). Only if none does, keycodes listing it in any column are used:
// grabbing a shifted symbol still requires the user to add Shift.
func (km keymap) keycodes(sym uint32) []xproto.Keycode {
	var first, any []xproto.Keycode
	if km.per == 0 {
		return nil
	}
	n := len(km.keysyms) / km.per
	for i := 0; i < n; i++ {
		kc := km.min + xproto.Keycode(i)
		for col, s := range km.symsOf(kc) {
			if uint32(s) != sym {
				continue
			}
			if col == 0 {
				first = append(first, kc)
			} else {
				any = append(any, kc)
			}
			break
		}
	}
	if len(first) > 0 {
		return first
	}
	return any
}

// mask converts contract modifiers to an X modifier mask.
func (km keymap) mask(m contracts.Modifier) uint16 {
	var out uint16
	if m&contracts.ModShift != 0 {
		out |= km.shift
	}
	if m&contracts.ModCtrl != 0 {
		out |= km.ctrl
	}
	if m&contracts.ModAlt != 0 {
		out |= km.alt
	}
	if m&contracts.ModSuper != 0 {
		out |= km.super
	}
	return out
}
