package hotkeys

import "github.com/xlsft/openclick/pkg/contracts"

// repeatFilter turns raw key events of grabbed keys into one press and one
// release per physical key stroke.
//
// Classic X autorepeat sends KeyRelease+KeyPress pairs carrying the same
// server timestamp while the key is held. A release is therefore held back
// until the next event for that key shows whether it was real: a press
// with the same timestamp cancels it, anything else (or expire, after a
// short delay) lets it through. Servers with XKB detectable autorepeat send
// repeated presses without releases; those are dropped because the key is
// already down. xgb has no XKB binding, so both cases are handled here.
type repeatFilter struct {
	down    map[byte]contracts.HotkeyID // keycode -> binding of a matched press
	pending map[byte]uint32             // keycode -> timestamp of a held-back release
}

func newRepeatFilter() *repeatFilter {
	return &repeatFilter{down: map[byte]contracts.HotkeyID{}, pending: map[byte]uint32{}}
}

// press handles a KeyPress; id is the binding it matched, or "" if none.
func (f *repeatFilter) press(code byte, ts uint32, id contracts.HotkeyID) []contracts.HotkeyEvent {
	var out []contracts.HotkeyEvent
	if pts, ok := f.pending[code]; ok {
		delete(f.pending, code)
		if pts == ts {
			return nil // autorepeat: the key never went up
		}
		out = append(out, contracts.HotkeyEvent{ID: f.down[code], Kind: contracts.HotkeyReleased})
		delete(f.down, code)
	}
	if _, held := f.down[code]; held || id == "" {
		return out
	}
	f.down[code] = id
	return append(out, contracts.HotkeyEvent{ID: id, Kind: contracts.HotkeyPressed})
}

// release handles a KeyRelease. It never emits directly; see expire.
func (f *repeatFilter) release(code byte, ts uint32) {
	if _, ok := f.down[code]; ok {
		f.pending[code] = ts
	}
}

// hasPending reports whether expire has releases to deliver.
func (f *repeatFilter) hasPending() bool { return len(f.pending) > 0 }

// expire delivers held-back releases once no repeat press followed them.
func (f *repeatFilter) expire() []contracts.HotkeyEvent {
	var out []contracts.HotkeyEvent
	for code := range f.pending {
		out = append(out, contracts.HotkeyEvent{ID: f.down[code], Kind: contracts.HotkeyReleased})
		delete(f.down, code)
		delete(f.pending, code)
	}
	return out
}

// reset forgets all key state, e.g. after the bindings changed.
func (f *repeatFilter) reset() {
	clear(f.down)
	clear(f.pending)
}
