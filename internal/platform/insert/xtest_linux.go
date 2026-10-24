//go:build linux

package insert

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/xlsft/companion/internal/platform/keys"
	"github.com/xlsft/companion/pkg/contracts"
)

// xtestTyper types on X11 with XTest fake key events. The characters of
// each run are mapped onto keycodes the layout leaves empty, typed, and
// the keycodes emptied again, so any Unicode text arrives regardless of
// layout and the user's own keys never change.
type xtestTyper struct {
	mu   sync.Mutex
	c    *xgb.Conn
	root xproto.Window
	min  xproto.Keycode
	max  xproto.Keycode
	// settle lets clients refresh their keyboard tables after a mapping
	// change before the first key that needs it arrives.
	settle time.Duration
}

func newXTest() *xtestTyper { return &xtestTyper{settle: 30 * time.Millisecond} }

func (x *xtestTyper) Name() string { return "x11-xtest" }

const (
	xKeyPress   = 2 // XTest FakeInput event types
	xKeyRelease = 3
)

func (x *xtestTyper) Ready(ctx context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.c != nil {
		return nil
	}
	if os.Getenv("DISPLAY") == "" {
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	c, err := xgb.NewConn()
	if err != nil {
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	if err := xtest.Init(c); err != nil {
		c.Close()
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	if _, err := xtest.GetVersion(c, 2, 2).Reply(); err != nil {
		c.Close()
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	setup := xproto.Setup(c)
	x.c, x.root, x.min, x.max = c, setup.DefaultScreen(c).Root, setup.MinKeycode, setup.MaxKeycode
	if _, _, err := x.spare(); err != nil {
		x.c.Close()
		x.c = nil
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	return ctx.Err()
}

var errNoSpare = errors.New("insert: no spare keycodes")

// spare is the longest run of keycodes with no keysyms, and the server's
// keysyms per keycode.
func (x *xtestTyper) spare() (first xproto.Keycode, n int, err error) {
	count := int(x.max) - int(x.min) + 1
	r, err := xproto.GetKeyboardMapping(x.c, x.min, byte(count)).Reply()
	if err != nil {
		return 0, 0, err
	}
	per := int(r.KeysymsPerKeycode)
	if per == 0 {
		return 0, 0, errNoSpare
	}
	best, bestN, cur, curN := 0, 0, 0, 0
	for i := 0; i < count && (i+1)*per <= len(r.Keysyms); i++ {
		empty := true
		for _, s := range r.Keysyms[i*per : (i+1)*per] {
			if s != 0 {
				empty = false
				break
			}
		}
		switch {
		case !empty:
			curN = 0
		case curN == 0:
			cur, curN = i, 1
		default:
			curN++
		}
		if curN > bestN {
			best, bestN = cur, curN
		}
	}
	if bestN == 0 {
		return 0, 0, errNoSpare
	}
	return x.min + xproto.Keycode(best), bestN, nil
}

// remap puts syms on the keycodes from first, two columns each so Shift
// makes no difference; nil syms of length n empties them.
func (x *xtestTyper) remap(first xproto.Keycode, syms []uint32, n int) error {
	ks := make([]xproto.Keysym, 2*n)
	for i, s := range syms {
		ks[2*i], ks[2*i+1] = xproto.Keysym(s), xproto.Keysym(s)
	}
	return xproto.ChangeKeyboardMappingChecked(x.c, byte(n), first, 2, ks).Check()
}

func (x *xtestTyper) fake(kind byte, code xproto.Keycode) {
	xtest.FakeInput(x.c, kind, byte(code), 0, x.root, 0, 0, 0)
}

// sync waits until the server has handled everything sent so far.
func (x *xtestTyper) sync() error {
	_, err := xproto.GetInputFocus(x.c).Reply()
	return err
}

func (x *xtestTyper) Type(ctx context.Context, text string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.c == nil {
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	first, n, err := x.spare()
	if err != nil {
		return err
	}
	n = min(n, maxKeysPerMap)
	defer func() {
		_ = x.remap(first, nil, n)
		_ = x.sync()
	}()
	for _, chunk := range chunks(Keysyms(text), n) {
		syms := distinct(chunk)
		code := map[uint32]xproto.Keycode{}
		for i, s := range syms {
			code[s] = first + xproto.Keycode(i)
		}
		if err := x.remap(first, syms, len(syms)); err != nil {
			return err
		}
		if err := sleep(ctx, x.settle); err != nil {
			return err
		}
		for _, s := range chunk {
			x.fake(xKeyPress, code[s])
			x.fake(xKeyRelease, code[s])
		}
		if err := x.sync(); err != nil {
			return err
		}
		// The next remap must not overtake keys still queued in clients.
		if err := sleep(ctx, x.settle); err != nil {
			return err
		}
	}
	return nil
}

// Chord presses the layout's own Control_L, so the modifier state is
// real, and the key through a spare keycode.
func (x *xtestTyper) Chord(ctx context.Context, h contracts.Hotkey) error {
	sym, ok := keys.Keysym(h.Key)
	if !ok || h.Mods&^contracts.ModCtrl != 0 {
		return contracts.Unsupported(contracts.CapInputInjection)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.c == nil {
		return contracts.Unsupported(contracts.CapInputInjection)
	}
	var ctrl xproto.Keycode
	if h.Mods&contracts.ModCtrl != 0 {
		var err error
		if ctrl, err = x.codeOf(symControlL); err != nil {
			return err
		}
	}
	first, _, err := x.spare()
	if err != nil {
		return err
	}
	defer func() {
		_ = x.remap(first, nil, 1)
		_ = x.sync()
	}()
	if err := x.remap(first, []uint32{sym}, 1); err != nil {
		return err
	}
	if err := sleep(ctx, x.settle); err != nil {
		return err
	}
	if ctrl != 0 {
		x.fake(xKeyPress, ctrl)
	}
	x.fake(xKeyPress, first)
	x.fake(xKeyRelease, first)
	if ctrl != 0 {
		x.fake(xKeyRelease, ctrl)
	}
	if err := x.sync(); err != nil {
		return err
	}
	return sleep(ctx, x.settle)
}

// codeOf finds the keycode the layout has sym on.
func (x *xtestTyper) codeOf(sym uint32) (xproto.Keycode, error) {
	count := int(x.max) - int(x.min) + 1
	r, err := xproto.GetKeyboardMapping(x.c, x.min, byte(count)).Reply()
	if err != nil {
		return 0, err
	}
	per := int(r.KeysymsPerKeycode)
	for i, s := range r.Keysyms {
		if uint32(s) == sym && per > 0 {
			return x.min + xproto.Keycode(i/per), nil
		}
	}
	return 0, contracts.Unsupported(contracts.CapInputInjection)
}

func (x *xtestTyper) Faithful(string) bool { return true }

func (x *xtestTyper) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.c != nil {
		x.c.Close()
		x.c = nil
	}
	return nil
}
