package hotkeys

import (
	"context"
	"strconv"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/xlsft/companion/internal/doubletap"
	"github.com/xlsft/companion/pkg/contracts"
)

// A double tap of a lone modifier (OC-133) cannot be grabbed: a passive
// grab on Control_L would take the keyboard for the whole press and break
// Ctrl+C in every app. XInput2 raw events or XRecord would deliver the key
// stream, but xgb has no XInput2 binding and drops generic events longer
// than 32 bytes (raw events always carry a valuator mask), and it cannot
// read XRecord's stream of replies to one EnableContext. So while a
// double-tap key is set, the core keyboard state is polled with
// QueryKeymap, which sees every key whatever has focus. A tap lasts 50 ms
// or more, so a 20 ms period sees it; one tiny round trip per period is
// cheap, and nothing polls while the feature is off.
const tapPoll = 20 * time.Millisecond

// tapClass is the detector's name for keycode code: a doubletap.Keys
// entry for modifier keys, "" for any other key.
func (km keymap) tapClass(code xproto.Keycode) string {
	for _, s := range km.symsOf(code) {
		switch s {
		case 0xffe3, 0xffe4: // Control_L, Control_R
			return "ctrl"
		case 0xffe1, 0xffe2: // Shift_L, Shift_R
			return "shift"
		case symAltL, symAltR, symMetaL, symMetaR:
			return "alt"
		case symSuperL, symSuperR:
			return "super"
		}
	}
	return ""
}

// tapScan turns successive QueryKeymap bitmaps into detector events.
type tapScan struct {
	det  *doubletap.Detector
	prev [32]byte
	// primed: prev holds a real reading. The first one only records what
	// is already held, so a key down at start is not a press.
	primed bool
}

// feed handles one reading and reports whether it completed a double tap.
// Presses are fed before releases: when a key goes down in the same
// period as the tapped key goes up, the order is unknown, and assuming a
// chord is the safe side.
func (t *tapScan) feed(cur [32]byte, at time.Time, class func(xproto.Keycode) string) bool {
	if !t.primed {
		t.prev, t.primed = cur, true
		return false
	}
	// Other keys keep their keycode in the name, so releasing one of two
	// held keys still leaves the other down for the detector.
	name := func(code int) string {
		if c := class(xproto.Keycode(code)); c != "" {
			return c
		}
		return "key" + strconv.Itoa(code)
	}
	fired := false
	for _, downs := range []bool{true, false} {
		for i := range 32 {
			changed := t.prev[i] ^ cur[i]
			for b := range 8 {
				if changed&(1<<b) == 0 || (cur[i]&(1<<b) != 0) != downs {
					continue
				}
				code := i*8 + b
				if downs {
					t.det.Down(name(code), at)
				} else if t.det.Up(name(code), at) {
					fired = true
				}
			}
		}
	}
	t.prev = cur
	return fired
}

// SetDoubleTap starts or stops watching key for a double tap that toggles
// id; "" stops. It implements contracts.DoubleTapper.
func (s *Service) SetDoubleTap(ctx context.Context, id contracts.HotkeyID, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := doubletap.Check(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tapStop != nil {
		close(s.tapStop)
		s.tapStop = nil
	}
	if key == "" {
		return nil
	}
	stop := make(chan struct{})
	s.tapStop = stop
	go s.pollTaps(stop, id, doubletap.New(key))
	return nil
}

func (s *Service) pollTaps(stop <-chan struct{}, id contracts.HotkeyID, det *doubletap.Detector) {
	tick := time.NewTicker(tapPoll)
	defer tick.Stop()
	scan := &tapScan{det: det}
	class := func(code xproto.Keycode) string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.km.tapClass(code)
	}
	for {
		select {
		case <-stop:
			return
		case <-s.done:
			return
		case <-tick.C:
		}
		r, err := xproto.QueryKeymap(s.conn).Reply()
		if err != nil || r == nil || len(r.Keys) != 32 {
			// A missed reading cannot be told from a press or release.
			scan.primed = false
			det.Reset()
			continue
		}
		if scan.feed([32]byte(r.Keys), time.Now(), class) {
			select {
			case s.taps <- contracts.HotkeyEvent{ID: id, Kind: contracts.HotkeyToggled}:
			default:
			}
		}
	}
}
