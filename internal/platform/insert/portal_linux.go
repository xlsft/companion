//go:build linux

package insert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/xlsft/companion/internal/platform/keys"
	"github.com/xlsft/companion/internal/platform/linuxwayland/portal"
	"github.com/xlsft/companion/pkg/contracts"
)

// portalTyper types through the RemoteDesktop portal's keyboard. The
// first session shows the desktop's consent dialog; the restore token it
// returns is kept in TokenPath so later sessions start without asking.
// One session lives as long as the app, so consent is asked once per run
// even where the backend gives no token.
type portalTyper struct {
	tokenPath string

	mu   sync.Mutex
	c    *portal.Conn
	sess dbus.ObjectPath
}

func newPortalTyper(tokenPath string) *portalTyper { return &portalTyper{tokenPath: tokenPath} }

func (p *portalTyper) Name() string { return "xdg-portal:RemoteDesktop" }

const (
	rdKeyboard     = 1
	persistAlways  = 2 // persist_mode: until the user revokes it
	rdConsentLimit = 2 * time.Minute
)

func (p *portalTyper) Ready(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess != "" {
		return nil
	}
	if p.c == nil {
		c, err := portal.Connect()
		if err != nil {
			return contracts.Unsupported(contracts.CapTextInsertion)
		}
		p.c = c
	}
	ver := p.c.Version(portal.IfRemoteDesktop)
	if ver == 0 {
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	// Backends without a compositor API behind them offer no devices
	// (niri, OC-6): asking would only fail after a dialog, or without one.
	if v, err := p.c.Property(portal.IfRemoteDesktop, "AvailableDeviceTypes"); err == nil {
		if n, ok := v.Value().(uint32); ok && n&rdKeyboard == 0 {
			return contracts.Unsupported(contracts.CapTextInsertion)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, rdConsentLimit)
	defer cancel()
	sess, err := p.c.CreateSession(ctx, portal.IfRemoteDesktop, nil)
	if err != nil {
		return p.mapErr(ctx, err)
	}
	opts := map[string]dbus.Variant{"types": dbus.MakeVariant(uint32(rdKeyboard))}
	if ver >= 2 {
		opts["persist_mode"] = dbus.MakeVariant(uint32(persistAlways))
		if tok := p.token(); tok != "" {
			opts["restore_token"] = dbus.MakeVariant(tok)
		}
	}
	if _, err := p.c.Request(ctx, portal.IfRemoteDesktop+".SelectDevices", opts, sess); err != nil {
		_ = p.c.CloseSession(sess)
		return p.mapErr(ctx, err)
	}
	res, err := p.c.Request(ctx, portal.IfRemoteDesktop+".Start", nil, sess, "")
	if err != nil {
		_ = p.c.CloseSession(sess)
		return p.mapErr(ctx, err)
	}
	if d, ok := res["devices"].Value().(uint32); ok && d&rdKeyboard == 0 {
		// The user allowed the pointer only.
		_ = p.c.CloseSession(sess)
		return contracts.PermissionDenied(contracts.CapTextInsertion)
	}
	if tok, ok := res["restore_token"].Value().(string); ok && tok != "" {
		p.saveToken(tok)
	}
	p.sess = sess
	return nil
}

func (p *portalTyper) mapErr(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, portal.ErrCancelled):
		return contracts.PermissionDenied(contracts.CapTextInsertion)
	case errors.Is(err, portal.ErrFailed):
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	return err
}

// token is the stored restore token, "" without one. Tokens are single
// use: each Start returns the next one.
func (p *portalTyper) token() string {
	if p.tokenPath == "" {
		return ""
	}
	b, err := os.ReadFile(p.tokenPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (p *portalTyper) saveToken(tok string) {
	if p.tokenPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p.tokenPath), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p.tokenPath, []byte(tok), 0o600)
}

func (p *portalTyper) keysym(ctx context.Context, sym uint32, down bool) error {
	state := uint32(0)
	if down {
		state = 1
	}
	return p.c.Call(ctx, portal.IfRemoteDesktop+".NotifyKeyboardKeysym", p.sess, map[string]dbus.Variant{}, int32(sym), state).Err
}

// dropLocked forgets a session the portal no longer accepts, so the next
// insertion starts a new one.
func (p *portalTyper) dropLocked() {
	if p.sess != "" {
		_ = p.c.CloseSession(p.sess)
		p.sess = ""
	}
}

func (p *portalTyper) Type(ctx context.Context, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess == "" {
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	for _, s := range Keysyms(text) {
		err := p.keysym(ctx, s, true)
		if err == nil {
			err = p.keysym(ctx, s, false)
		}
		if err != nil {
			p.dropLocked()
			return err
		}
	}
	return nil
}

func (p *portalTyper) Chord(ctx context.Context, h contracts.Hotkey) error {
	sym, ok := keys.Keysym(h.Key)
	if !ok || h.Mods&^contracts.ModCtrl != 0 {
		return contracts.Unsupported(contracts.CapInputInjection)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess == "" {
		return contracts.Unsupported(contracts.CapInputInjection)
	}
	type key struct {
		sym  uint32
		down bool
	}
	seq := []key{{sym, true}, {sym, false}}
	if h.Mods&contracts.ModCtrl != 0 {
		seq = append([]key{{symControlL, true}}, append(seq, key{symControlL, false})...)
	}
	for _, k := range seq {
		if err := p.keysym(ctx, k.sym, k.down); err != nil {
			p.dropLocked()
			return err
		}
	}
	return nil
}

// Faithful: GNOME and KDE type a keysym only when the active layout has
// it, so only ASCII is sure to arrive.
func (p *portalTyper) Faithful(text string) bool { return asciiOnly(text) }

func (p *portalTyper) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c == nil {
		return nil
	}
	p.dropLocked()
	err := p.c.Close()
	p.c = nil
	return err
}
