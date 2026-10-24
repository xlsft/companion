//go:build linux

package insert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/internal/platform/keys"
	"github.com/xlsft/companion/internal/platform/linuxwayland/wl"
	"github.com/xlsft/companion/pkg/contracts"
)

// vkbd types through zwp_virtual_keyboard_v1. The compositor asks for no
// consent, so it is used only for text the user dictated with their own
// shortcut. Each insertion uploads a keymap holding exactly the characters
// it types, so any Unicode text comes out right whatever the user's
// layout; the device is destroyed afterwards and the user's keymap
// applies again.
type vkbd struct {
	mu   sync.Mutex
	c    *wl.Conn
	kb   uint32
	t0   time.Time
	wait time.Duration // between key events, so no client drops keys
}

func newVirtualKeyboard() *vkbd { return &vkbd{t0: time.Now(), wait: 2 * time.Millisecond} }

func (v *vkbd) Name() string { return detect.BackendVirtualKeyboard }

// Wayland object protocol constants.
const (
	opGetRegistry   = 1 // wl_display.get_registry
	opBind          = 0 // wl_registry.bind
	opCreateVKbd    = 0 // zwp_virtual_keyboard_manager_v1.create_virtual_keyboard
	opVKKeymap      = 0 // zwp_virtual_keyboard_v1.keymap
	opVKKey         = 1
	opVKModifiers   = 2
	opVKDestroy     = 3
	keymapFormatXKB = 1
	maxKeysPerMap   = 200 // X clients behind Xwayland see keycodes up to 255
)

func (v *vkbd) Ready(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.connectLocked(ctx)
}

func (v *vkbd) connectLocked(ctx context.Context) error {
	if v.c != nil {
		select {
		case <-v.c.Done():
			v.c = nil
		default:
			return nil
		}
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("WAYLAND_SOCKET") == "" {
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	c, err := wl.Connect()
	if err != nil {
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	type global struct {
		name, version uint32
	}
	var mu sync.Mutex
	globals := map[string]global{}
	reg := c.NewObject(func(op uint16, ev *wl.Event) {
		if op != 0 {
			return
		}
		name, iface, ver := ev.Uint(), ev.String(), ev.Uint()
		mu.Lock()
		if _, ok := globals[iface]; !ok {
			globals[iface] = global{name, ver}
		}
		mu.Unlock()
	})
	if err := c.Send(wl.DisplayID, opGetRegistry, wl.NewMsg().NewID(reg)); err != nil {
		c.Close()
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	if err := c.Roundtrip(ctx); err != nil {
		c.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	mu.Lock()
	seatG, okSeat := globals["wl_seat"]
	mgrG, okMgr := globals["zwp_virtual_keyboard_manager_v1"]
	mu.Unlock()
	if !okSeat || !okMgr {
		c.Close()
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	seat := c.NewObject(nil)
	mgr := c.NewObject(nil)
	kb := c.NewObject(nil)
	errs := []error{
		c.Send(reg, opBind, wl.NewMsg().Uint(seatG.name).String("wl_seat").Uint(1).NewID(seat)),
		c.Send(reg, opBind, wl.NewMsg().Uint(mgrG.name).String("zwp_virtual_keyboard_manager_v1").Uint(1).NewID(mgr)),
		c.Send(mgr, opCreateVKbd, wl.NewMsg().Object(seat).NewID(kb)),
	}
	if err := errors.Join(errs...); err == nil {
		err = c.Roundtrip(ctx)
	}
	if err := c.Err(); err != nil {
		c.Close()
		// The manager's only error is "unauthorized": the compositor
		// restricts virtual keyboards (e.g. to sandboxed clients' peers).
		var pe *wl.ProtocolError
		if errors.As(err, &pe) {
			return contracts.PermissionDenied(contracts.CapTextInsertion)
		}
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	if ctx.Err() != nil {
		c.Close()
		return ctx.Err()
	}
	v.c, v.kb = c, kb
	return nil
}

// closeLocked destroys the device, so the compositor drops its keymap.
func (v *vkbd) closeLocked() {
	if v.c == nil {
		return
	}
	_ = v.c.Send(v.kb, opVKDestroy, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = v.c.Roundtrip(ctx)
	cancel()
	v.c.Close()
	v.c = nil
}

func (v *vkbd) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closeLocked()
	return nil
}

func (v *vkbd) Faithful(string) bool { return true }

func (v *vkbd) Type(ctx context.Context, text string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.connectLocked(ctx); err != nil {
		return err
	}
	defer v.closeLocked()
	for _, chunk := range chunks(Keysyms(text), maxKeysPerMap) {
		syms := distinct(chunk)
		code := map[uint32]uint32{}
		for i, s := range syms {
			code[s] = uint32(i + 1)
		}
		if err := v.keymapLocked(ctx, syms, 0); err != nil {
			return err
		}
		for i, s := range chunk {
			if err := v.tapLocked(ctx, code[s]); err != nil {
				return err
			}
			if i%32 == 31 {
				if err := v.c.Roundtrip(ctx); err != nil {
					return err
				}
			}
		}
		if err := v.c.Roundtrip(ctx); err != nil {
			return err
		}
	}
	return nil
}

// keymapLocked uploads a keymap for syms and resets the modifiers to mods.
func (v *vkbd) keymapLocked(ctx context.Context, syms []uint32, mods uint32) error {
	km := keymap(syms) + "\x00"
	fd, err := unix.MemfdCreate("companion-keymap", unix.MFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("insert: keymap memfd: %w", err)
	}
	defer unix.Close(fd)
	if _, err := unix.Write(fd, []byte(km)); err != nil {
		return fmt.Errorf("insert: keymap write: %w", err)
	}
	if err := v.c.Send(v.kb, opVKKeymap, wl.NewMsg().Uint(keymapFormatXKB).Fd(fd).Uint(uint32(len(km)))); err != nil {
		return err
	}
	if err := v.c.Send(v.kb, opVKModifiers, wl.NewMsg().Uint(mods).Uint(0).Uint(0).Uint(0)); err != nil {
		return err
	}
	if err := v.c.Roundtrip(ctx); err != nil {
		return err
	}
	// Clients apply a new keymap when its first key arrives; a moment's
	// pause keeps toolkits that rebuild their tables from racing it.
	return sleep(ctx, 20*time.Millisecond)
}

func (v *vkbd) keyLocked(code uint32, down bool) error {
	state := uint32(0)
	if down {
		state = 1
	}
	ms := uint32(time.Since(v.t0).Milliseconds())
	return v.c.Send(v.kb, opVKKey, wl.NewMsg().Uint(ms).Uint(code).Uint(state))
}

func (v *vkbd) tapLocked(ctx context.Context, code uint32) error {
	if err := v.keyLocked(code, true); err != nil {
		return err
	}
	if err := v.keyLocked(code, false); err != nil {
		return err
	}
	return sleep(ctx, v.wait)
}

// Chord supports Ctrl (the paste chord) plus one key.
func (v *vkbd) Chord(ctx context.Context, h contracts.Hotkey) error {
	sym, ok := keys.Keysym(h.Key)
	if !ok || h.Mods&^contracts.ModCtrl != 0 {
		return contracts.Unsupported(contracts.CapInputInjection)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.connectLocked(ctx); err != nil {
		return err
	}
	defer v.closeLocked()
	syms := []uint32{sym}
	ctrl := h.Mods&contracts.ModCtrl != 0
	if ctrl {
		syms = append(syms, symControlL)
	}
	if err := v.keymapLocked(ctx, syms, 0); err != nil {
		return err
	}
	const ctrlMask = 4 // Control is modifier index 2 in every xkb keymap
	if ctrl {
		if err := v.keyLocked(2, true); err != nil {
			return err
		}
		if err := v.c.Send(v.kb, opVKModifiers, wl.NewMsg().Uint(ctrlMask).Uint(0).Uint(0).Uint(0)); err != nil {
			return err
		}
	}
	if err := v.tapLocked(ctx, 1); err != nil {
		return err
	}
	if ctrl {
		if err := v.keyLocked(2, false); err != nil {
			return err
		}
		if err := v.c.Send(v.kb, opVKModifiers, wl.NewMsg().Uint(0).Uint(0).Uint(0).Uint(0)); err != nil {
			return err
		}
	}
	return v.c.Roundtrip(ctx)
}
