// Package doubletap turns key-down/up events, or clean taps reported by a
// compositor, into "key K was tapped twice quickly" (OC-133). It is pure
// logic over caller-supplied timestamps, so backends feed it whatever
// their system delivers and tests drive it with synthetic timings.
package doubletap

import (
	"fmt"
	"slices"
	"time"
)

// Keys are the keys a double tap can be set on: lone modifiers, which are
// the only keys a user taps on their own without typing something.
var Keys = []string{"ctrl", "shift", "alt", "super"}

// Valid reports whether key is "" (off) or one of Keys.
func Valid(key string) bool { return key == "" || slices.Contains(Keys, key) }

// Check returns an error for a key Valid rejects; the message names the
// setting, not the input.
func Check(key string) error {
	if !Valid(key) {
		return fmt.Errorf("doubletap: unknown key")
	}
	return nil
}

// Timings. Window is generous enough for a relaxed double tap and short
// enough that two separate Ctrl uses a moment apart do not count.
const (
	// Window is the longest gap from the first release to the second
	// press (Down/Up) or between the two releases (Tap).
	Window = 400 * time.Millisecond
	// MaxHold is the longest a press may last and still be a tap: holding
	// Ctrl to use the mouse is not one.
	MaxHold = 350 * time.Millisecond
	// MinGap is the shortest gap between taps: anything faster is contact
	// bounce of one press, not a second tap.
	MinGap = 30 * time.Millisecond
)

// Detector watches one key. The zero value is unusable; use New.
type Detector struct {
	key  string
	down map[string]bool

	pressAt time.Time // when key went down
	tainted bool      // another key was down during this press of key
	armed   bool      // one clean tap done, waiting for the second
	firstUp time.Time // release of the first tap
	// bounced: this press is the bounce of the first, so its release ends
	// the first tap again instead of completing a double.
	bounced bool
}

// New watches key, one of Keys.
func New(key string) *Detector { return &Detector{key: key, down: map[string]bool{}} }

// Key is the watched key.
func (d *Detector) Key() string { return d.key }

// Reset forgets every pressed key and a pending first tap, e.g. after the
// event source lost track of the keyboard.
func (d *Detector) Reset() {
	clear(d.down)
	d.tainted, d.armed, d.bounced = false, false, false
}

// Down handles a press of key k; any name other than the watched key
// stands for "some other key". A press of a key already down (autorepeat)
// is ignored.
func (d *Detector) Down(k string, at time.Time) {
	if d.down[k] {
		return
	}
	d.down[k] = true
	if k != d.key {
		// Typing between the taps, or a chord with the key held: neither
		// is a double tap.
		d.armed = false
		if d.down[d.key] {
			d.tainted = true
		}
		return
	}
	d.tainted = len(d.down) > 1 // another key was already held: a chord
	d.bounced = false
	if d.armed {
		gap := at.Sub(d.firstUp)
		switch {
		case gap > Window:
			d.armed = false
		case gap < MinGap:
			d.bounced = true
			return // keep pressAt of the first press
		}
	}
	d.pressAt = at
}

// Up handles a release of key k and reports whether it completed a double
// tap of the watched key.
func (d *Detector) Up(k string, at time.Time) bool {
	if !d.down[k] {
		return false
	}
	delete(d.down, k)
	if k != d.key {
		return false
	}
	clean := !d.tainted && at.Sub(d.pressAt) <= MaxHold
	switch {
	case !clean:
		d.armed = false
	case d.armed && !d.bounced:
		d.armed = false
		return true
	default:
		d.armed, d.firstUp = true, at
	}
	return false
}

// Tap handles a clean tap of the watched key reported as one event by a
// source that only sends those (a compositor release bind), at the time
// of its release. It reports whether the tap completed a double tap.
func (d *Detector) Tap(at time.Time) bool {
	if d.armed {
		gap := at.Sub(d.firstUp)
		if gap < MinGap {
			return false // the same tap reported twice
		}
		if gap <= Window {
			d.armed = false
			return true
		}
	}
	d.armed, d.firstUp = true, at
	return false
}
