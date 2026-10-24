//go:build linux

// Package wl is a minimal pure-Go Wayland client: wire encoding, fd passing
// and object dispatch. Protocol objects are driven by raw opcodes from the
// callers; only the handful of interfaces the overlay and capture bridges
// need are used, so a code generator would cost more than it saves.
package wl

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// DisplayID is the fixed object id of wl_display.
const DisplayID = 1

// Handler receives events for one object. It runs on the connection's read
// goroutine and must not block on further events of the same connection.
type Handler func(opcode uint16, ev *Event)

// Conn is a Wayland client connection.
type Conn struct {
	sock *net.UnixConn

	wmu sync.Mutex // serialises writes so messages and their fds stay together

	mu       sync.Mutex
	handlers map[uint32]Handler
	nextID   uint32
	free     []uint32
	err      error

	done chan struct{}
}

// Connect opens $WAYLAND_DISPLAY (relative to $XDG_RUNTIME_DIR unless
// absolute) and starts the event loop.
func Connect() (*Conn, error) {
	if s := os.Getenv("WAYLAND_SOCKET"); s != "" {
		fd, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("wl: bad WAYLAND_SOCKET: %w", err)
		}
		f := os.NewFile(uintptr(fd), "wayland-socket")
		fc, err := net.FileConn(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("wl: WAYLAND_SOCKET: %w", err)
		}
		uc, ok := fc.(*net.UnixConn)
		if !ok {
			fc.Close()
			return nil, errors.New("wl: WAYLAND_SOCKET is not a unix socket")
		}
		return newConn(uc), nil
	}
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return nil, errors.New("wl: WAYLAND_DISPLAY not set")
	}
	path := name
	if !filepath.IsAbs(name) {
		rt := os.Getenv("XDG_RUNTIME_DIR")
		if rt == "" {
			return nil, errors.New("wl: XDG_RUNTIME_DIR not set")
		}
		path = filepath.Join(rt, name)
	}
	uc, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("wl: connect: %w", err)
	}
	return newConn(uc), nil
}

func newConn(uc *net.UnixConn) *Conn {
	c := &Conn{
		sock:     uc,
		handlers: map[uint32]Handler{},
		nextID:   2,
		done:     make(chan struct{}),
	}
	c.handlers[DisplayID] = c.handleDisplay
	go c.readLoop()
	return c
}

// Done is closed when the connection fails or is closed.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns the error that ended the connection, if any.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) Close() error {
	c.fail(errors.New("wl: connection closed"))
	return nil
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	c.mu.Unlock()
	c.sock.Close()
	close(c.done)
}

// NewObject allocates a client object id and registers h for its events.
// h may be nil for objects without events.
func (c *Conn) NewObject(h Handler) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var id uint32
	if n := len(c.free); n > 0 {
		id = c.free[n-1]
		c.free = c.free[:n-1]
	} else {
		id = c.nextID
		c.nextID++
	}
	if h == nil {
		h = func(uint16, *Event) {}
	}
	c.handlers[id] = h
	return id
}

// SetHandler replaces the handler of an existing object.
func (c *Conn) SetHandler(id uint32, h Handler) {
	c.mu.Lock()
	c.handlers[id] = h
	c.mu.Unlock()
}

// Forget drops the handler of an object the client destroyed. Its id is
// reused only after the server confirms with wl_display.delete_id.
func (c *Conn) Forget(id uint32) {
	c.mu.Lock()
	c.handlers[id] = func(uint16, *Event) {}
	c.mu.Unlock()
}

func (c *Conn) handleDisplay(op uint16, ev *Event) {
	switch op {
	case 0: // error(object_id, code, message)
		obj := ev.Uint()
		code := ev.Uint()
		msg := ev.String()
		c.fail(&ProtocolError{Object: obj, Code: code, Message: msg})
	case 1: // delete_id(id)
		id := ev.Uint()
		c.mu.Lock()
		if _, ok := c.handlers[id]; ok {
			delete(c.handlers, id)
			c.free = append(c.free, id)
		}
		c.mu.Unlock()
	}
}

// ProtocolError is a fatal wl_display.error from the compositor.
type ProtocolError struct {
	Object, Code uint32
	Message      string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("wl: protocol error on object %d code %d: %s", e.Object, e.Code, e.Message)
}

// Send writes one request. Fds in m are passed with SCM_RIGHTS and remain
// owned by the caller.
func (c *Conn) Send(id uint32, opcode uint16, m *Msg) error {
	var body []byte
	var fds []int
	if m != nil {
		body, fds = m.b, m.fds
	}
	size := 8 + len(body)
	if size > 0xffff {
		return errors.New("wl: message too large")
	}
	buf := make([]byte, size)
	binary.LittleEndian.PutUint32(buf[0:], id)
	binary.LittleEndian.PutUint32(buf[4:], uint32(size)<<16|uint32(opcode))
	copy(buf[8:], body)
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.Err(); err != nil {
		return err
	}
	if _, _, err := c.sock.WriteMsgUnix(buf, oob, nil); err != nil {
		c.fail(err)
		return err
	}
	return nil
}

// Sync sends wl_display.sync and returns a channel closed when the server
// has processed every earlier request.
func (c *Conn) Sync() (<-chan struct{}, error) {
	ch := make(chan struct{})
	var once sync.Once
	id := c.NewObject(func(op uint16, ev *Event) {
		once.Do(func() { close(ch) })
	})
	if err := c.Send(DisplayID, 0, NewMsg().NewID(id)); err != nil {
		return nil, err
	}
	return ch, nil
}

// Roundtrip blocks until the server processed all earlier requests and all
// events they caused have been dispatched.
func (c *Conn) Roundtrip(ctx context.Context) error {
	ch, err := c.Sync()
	if err != nil {
		return err
	}
	select {
	case <-ch:
		return nil
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Conn) readLoop() {
	buf := make([]byte, 0, 1<<16)
	tmp := make([]byte, 1<<16)
	oob := make([]byte, syscall.CmsgSpace(28*4))
	for {
		n, oobn, _, _, err := c.sock.ReadMsgUnix(tmp, oob)
		if err != nil {
			c.fail(fmt.Errorf("wl: read: %w", err))
			return
		}
		// None of the events this package consumes carry fds; close any we
		// receive so they don't leak.
		if oobn > 0 {
			if msgs, err := syscall.ParseSocketControlMessage(oob[:oobn]); err == nil {
				for _, m := range msgs {
					if fds, err := syscall.ParseUnixRights(&m); err == nil {
						for _, fd := range fds {
							syscall.Close(fd)
						}
					}
				}
			}
		}
		buf = append(buf, tmp[:n]...)
		for len(buf) >= 8 {
			id := binary.LittleEndian.Uint32(buf[0:])
			w := binary.LittleEndian.Uint32(buf[4:])
			size := int(w >> 16)
			op := uint16(w & 0xffff)
			if size < 8 {
				c.fail(errors.New("wl: malformed event"))
				return
			}
			if len(buf) < size {
				break
			}
			ev := &Event{b: buf[8:size]}
			c.mu.Lock()
			h := c.handlers[id]
			c.mu.Unlock()
			if h != nil {
				h(op, ev)
			}
			buf = buf[size:]
		}
		// Keep the backing array from growing without bound.
		if len(buf) == 0 {
			buf = buf[:0:cap(buf)]
		} else {
			buf = append(make([]byte, 0, 1<<16), buf...)
		}
		select {
		case <-c.done:
			return
		default:
		}
	}
}
