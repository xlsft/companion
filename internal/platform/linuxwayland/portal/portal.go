//go:build linux

// Package portal is a thin client for xdg-desktop-portal: the Request /
// Session handshake shared by GlobalShortcuts, ScreenCast, RemoteDesktop
// and Screenshot. It holds no policy; callers decide what to ask for.
package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	busName    = "org.freedesktop.portal.Desktop"
	objectPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	ifRequest  = "org.freedesktop.portal.Request"
	ifSession  = "org.freedesktop.portal.Session"

	IfGlobalShortcuts = "org.freedesktop.portal.GlobalShortcuts"
	IfScreenCast      = "org.freedesktop.portal.ScreenCast"
	IfRemoteDesktop   = "org.freedesktop.portal.RemoteDesktop"
	IfScreenshot      = "org.freedesktop.portal.Screenshot"
)

// Response codes of org.freedesktop.portal.Request.Response.
const (
	ResponseOK        = 0
	ResponseCancelled = 1 // user dismissed the dialog
	ResponseOther     = 2 // backend error or interaction ended otherwise
)

var (
	ErrCancelled = errors.New("portal: request cancelled by user")
	ErrFailed    = errors.New("portal: request failed")
)

// Conn is a session-bus connection to the portal frontend.
type Conn struct {
	bus    *dbus.Conn
	obj    dbus.BusObject
	sender string // unique name in request-path form

	mu      sync.Mutex
	waiters map[string][]chan *dbus.Signal // key: path + "\x00" + member
	sigs    chan *dbus.Signal
}

func Connect() (*Conn, error) {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("portal: session bus: %w", err)
	}
	c := &Conn{
		bus:     bus,
		obj:     bus.Object(busName, objectPath),
		sender:  strings.ReplaceAll(strings.TrimPrefix(bus.Names()[0], ":"), ".", "_"),
		waiters: map[string][]chan *dbus.Signal{},
		sigs:    make(chan *dbus.Signal, 64),
	}
	bus.Signal(c.sigs)
	go c.dispatch()
	return c, nil
}

func (c *Conn) Close() error { return c.bus.Close() }

func (c *Conn) dispatch() {
	for s := range c.sigs {
		member := s.Name[strings.LastIndex(s.Name, ".")+1:]
		key := string(s.Path) + "\x00" + member
		c.mu.Lock()
		ws := append([]chan *dbus.Signal(nil), c.waiters[key]...)
		ws = append(ws, c.waiters["\x00"+member]...)
		c.mu.Unlock()
		for _, w := range ws {
			select {
			case w <- s:
			default:
			}
		}
	}
}

// Subscribe delivers signals named iface.member on path ("" = any path)
// until cancel is called.
func (c *Conn) Subscribe(path dbus.ObjectPath, iface, member string) (<-chan *dbus.Signal, func(), error) {
	opts := []dbus.MatchOption{dbus.WithMatchInterface(iface), dbus.WithMatchMember(member)}
	if path != "" {
		opts = append(opts, dbus.WithMatchObjectPath(path))
	}
	if err := c.bus.AddMatchSignal(opts...); err != nil {
		return nil, nil, err
	}
	ch := make(chan *dbus.Signal, 32)
	key := string(path) + "\x00" + member
	c.mu.Lock()
	c.waiters[key] = append(c.waiters[key], ch)
	c.mu.Unlock()
	cancel := func() {
		_ = c.bus.RemoveMatchSignal(opts...)
		c.mu.Lock()
		ws := c.waiters[key]
		for i, w := range ws {
			if w == ch {
				c.waiters[key] = append(ws[:i], ws[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
	}
	return ch, cancel, nil
}

// Version reads the version property of a portal interface; 0 means the
// interface is not exported.
func (c *Conn) Version(iface string) uint32 {
	v, err := c.obj.GetProperty(iface + ".version")
	if err != nil {
		return 0
	}
	u, _ := v.Value().(uint32)
	return u
}

// Property reads another property of a portal interface.
func (c *Conn) Property(iface, name string) (dbus.Variant, error) {
	return c.obj.GetProperty(iface + "." + name)
}

// Token returns a fresh handle/session token.
func Token() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "openclick_" + hex.EncodeToString(b)
}

func (c *Conn) requestPath(token string) dbus.ObjectPath {
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + c.sender + "/" + token)
}

// SessionPath predicts the session object path for a session token.
func (c *Conn) SessionPath(token string) dbus.ObjectPath {
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/session/" + c.sender + "/" + token)
}

// Request calls method, which must take options a{sv} as its last
// argument, and waits for the Request.Response. A handle_token is added to
// options so the response signal can be subscribed before the call, as the
// portal spec requires to avoid a race.
func (c *Conn) Request(ctx context.Context, method string, options map[string]dbus.Variant, args ...any) (map[string]dbus.Variant, error) {
	token := Token()
	if options == nil {
		options = map[string]dbus.Variant{}
	}
	options["handle_token"] = dbus.MakeVariant(token)
	path := c.requestPath(token)
	ch, cancel, err := c.Subscribe(path, ifRequest, "Response")
	if err != nil {
		return nil, err
	}
	defer cancel()
	var handle dbus.ObjectPath
	call := c.obj.CallWithContext(ctx, method, 0, append(args, options)...)
	if call.Err != nil {
		return nil, fmt.Errorf("portal: %s: %w", method, call.Err)
	}
	if err := call.Store(&handle); err != nil {
		return nil, fmt.Errorf("portal: %s: %w", method, err)
	}
	if handle != path {
		// Old frontends (< 0.9) ignore handle_token; follow the real path.
		cancel()
		ch, cancel, err = c.Subscribe(handle, ifRequest, "Response")
		if err != nil {
			return nil, err
		}
		defer cancel()
	}
	select {
	case s := <-ch:
		if len(s.Body) < 2 {
			return nil, ErrFailed
		}
		code, _ := s.Body[0].(uint32)
		res, _ := s.Body[1].(map[string]dbus.Variant)
		switch code {
		case ResponseOK:
			return res, nil
		case ResponseCancelled:
			return res, ErrCancelled
		default:
			return res, ErrFailed
		}
	case <-ctx.Done():
		// Closing the request dismisses any dialog the backend still shows.
		_ = c.bus.Object(busName, handle).Call(ifRequest+".Close", 0).Err
		return nil, ctx.Err()
	}
}

// Call invokes a portal method that returns directly (no Request).
func (c *Conn) Call(ctx context.Context, method string, args ...any) *dbus.Call {
	return c.obj.CallWithContext(ctx, method, 0, args...)
}

// CloseSession ends a portal session.
func (c *Conn) CloseSession(session dbus.ObjectPath) error {
	return c.bus.Object(busName, session).Call(ifSession+".Close", 0).Err
}

// CreateSession calls <iface>.CreateSession and returns the session handle.
func (c *Conn) CreateSession(ctx context.Context, iface string, extra map[string]dbus.Variant) (dbus.ObjectPath, error) {
	opts := map[string]dbus.Variant{"session_handle_token": dbus.MakeVariant(Token())}
	for k, v := range extra {
		opts[k] = v
	}
	res, err := c.Request(ctx, iface+".CreateSession", opts)
	if err != nil {
		return "", err
	}
	switch v := res["session_handle"].Value().(type) {
	case string:
		return dbus.ObjectPath(v), nil
	case dbus.ObjectPath:
		return v, nil
	}
	return "", fmt.Errorf("portal: %s.CreateSession: no session_handle", iface)
}
