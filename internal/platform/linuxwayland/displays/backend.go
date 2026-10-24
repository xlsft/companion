// Package displays reads the display topology from a Wayland compositor
// using wl_output and zxdg_output_manager_v1 over a pure-Go wire client, and
// keeps listening for hotplug and mode changes.
package displays

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

const (
	maxOutputVersion = 4
	maxXDGVersion    = 3
	handshakeTimeout = 5 * time.Second

	idDisplay  = 1
	idRegistry = 2
	idSync     = 3 // first wl_callback; later ids are allocated after it
)

// Backend implements internal/displays.Backend for Wayland.
type Backend struct {
	conn net.Conn
	done chan struct{} // closed when the read loop exits

	wmu sync.Mutex // serialises writes

	// Read-loop state (single goroutine, no lock needed).
	nextID    uint32
	handlers  map[uint32]func(op uint16, r *argReader) error
	byGlobal  map[uint32]*output
	mgrGlobal uint32
	mgrID     uint32

	mu      sync.Mutex // guards what Displays reads
	outputs map[uint32]*output
	haveXDG bool
	err     error

	changed chan struct{}
	ready   chan struct{}
}

// Open connects to the compositor named by WAYLAND_DISPLAY and waits until
// the initial set of outputs is known.
func Open(ctx context.Context) (*Backend, error) {
	disp := os.Getenv("WAYLAND_DISPLAY")
	if disp == "" {
		return nil, errors.New("wayland: WAYLAND_DISPLAY not set")
	}
	path := disp
	if !filepath.IsAbs(path) {
		path = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), disp)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("wayland: connect: %w", err)
	}
	return newBackend(ctx, conn)
}

func newBackend(ctx context.Context, conn net.Conn) (*Backend, error) {
	b := &Backend{
		conn:     conn,
		done:     make(chan struct{}),
		nextID:   idSync + 1,
		handlers: map[uint32]func(uint16, *argReader) error{},
		byGlobal: map[uint32]*output{},
		outputs:  map[uint32]*output{},
		changed:  make(chan struct{}, 1),
		ready:    make(chan struct{}),
	}
	b.handlers[idDisplay] = b.onDisplay
	b.handlers[idRegistry] = b.onRegistry
	b.handlers[idSync] = func(uint16, *argReader) error { return b.afterRegistrySync() }

	go b.loop()
	if err := b.send(marshal(idDisplay, 1, uint32(idRegistry)), marshal(idDisplay, 0, uint32(idSync))); err != nil {
		b.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	select {
	case <-b.ready:
		return b, nil
	case <-b.done:
		err := b.failure()
		b.Close()
		return nil, err
	case <-ctx.Done():
		b.Close()
		return nil, fmt.Errorf("wayland: handshake: %w", ctx.Err())
	}
}

func (b *Backend) send(msgs ...[]byte) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	for _, m := range msgs {
		if _, err := b.conn.Write(m); err != nil {
			return fmt.Errorf("wayland: write: %w", err)
		}
	}
	return nil
}

func (b *Backend) newID() uint32 { id := b.nextID; b.nextID++; return id }

// Close drops the connection; Changed is closed shortly after.
func (b *Backend) Close() error {
	err := b.conn.Close()
	<-b.done
	return err
}

func (b *Backend) Changed() <-chan struct{} { return b.changed }

func (b *Backend) failure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		return errors.New("wayland: connection closed")
	}
	return b.err
}

func (b *Backend) signal() {
	select {
	case b.changed <- struct{}{}:
	default: // one pending signal already covers this change
	}
}

func (b *Backend) loop() {
	defer close(b.done)
	defer close(b.changed)
	r := bufio.NewReader(b.conn)
	for {
		obj, op, body, err := readMessage(r)
		if err == nil {
			if h := b.handlers[obj]; h != nil {
				err = h(op, &argReader{b: body})
			}
		}
		if err != nil {
			b.mu.Lock()
			b.err = fmt.Errorf("wayland: %w", err)
			b.mu.Unlock()
			return
		}
	}
}

func (b *Backend) onDisplay(op uint16, r *argReader) error {
	if op != opDisplayError {
		return nil // delete_id: ids are never reused here
	}
	obj, code, msg := r.u32(), r.u32(), r.str()
	return fmt.Errorf("protocol error on object %d code %d: %s", obj, code, msg)
}

func (b *Backend) onRegistry(op uint16, r *argReader) error {
	switch op {
	case opRegistryGlobal:
		name, iface, ver := r.u32(), r.str(), r.u32()
		if r.err != nil {
			return r.err
		}
		return b.onGlobal(name, iface, ver)
	case opRegistryGlobalRemove:
		name := r.u32()
		if r.err != nil {
			return r.err
		}
		return b.onGlobalRemove(name)
	}
	return nil
}

func (b *Backend) bind(name uint32, iface string, ver uint32) (uint32, error) {
	id := b.newID()
	return id, b.send(marshal(idRegistry, opRegistryBind, name, iface, ver, id))
}

func (b *Backend) onGlobal(name uint32, iface string, ver uint32) error {
	switch iface {
	case "wl_output":
		if ver < 2 { // no done event: cannot tell when state is complete
			return nil
		}
		v := min(ver, maxOutputVersion)
		id, err := b.bind(name, iface, v)
		if err != nil {
			return err
		}
		o := &output{global: name, id: id, version: v, factor: 1}
		b.byGlobal[name] = o
		b.handlers[id] = func(op uint16, r *argReader) error {
			return b.apply(func() error { return o.onOutputEvent(op, r) })
		}
		b.mu.Lock()
		b.outputs[name] = o
		b.mu.Unlock()
		return b.ensureXDG(o)
	case "zxdg_output_manager_v1":
		if b.mgrID != 0 {
			return nil
		}
		id, err := b.bind(name, iface, min(ver, maxXDGVersion))
		if err != nil {
			return err
		}
		b.mgrGlobal, b.mgrID = name, id
		b.mu.Lock()
		b.haveXDG = true
		b.mu.Unlock()
		for _, o := range b.byGlobal {
			if err := b.ensureXDG(o); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureXDG attaches an xdg_output to o once the manager is known.
func (b *Backend) ensureXDG(o *output) error {
	if b.mgrID == 0 || o.xdgID != 0 {
		return nil
	}
	o.xdgID = b.newID()
	b.handlers[o.xdgID] = func(op uint16, r *argReader) error {
		return b.apply(func() error { return o.onXDGEvent(op, r) })
	}
	return b.send(marshal(b.mgrID, opXDGManagerGetOutput, o.xdgID, o.id))
}

func (b *Backend) onGlobalRemove(name uint32) error {
	if name == b.mgrGlobal && b.mgrID != 0 {
		return nil // outputs keep their last logical geometry
	}
	o := b.byGlobal[name]
	if o == nil {
		return nil
	}
	delete(b.byGlobal, name)
	delete(b.handlers, o.id)
	b.mu.Lock()
	delete(b.outputs, name)
	b.mu.Unlock()
	var reqs [][]byte
	if o.xdgID != 0 {
		delete(b.handlers, o.xdgID)
		reqs = append(reqs, marshal(o.xdgID, opXDGOutputDestroy))
	}
	if o.version >= 3 {
		reqs = append(reqs, marshal(o.id, opOutputRelease))
	}
	b.signal()
	return b.send(reqs...)
}

// apply mutates output state under the lock Displays reads with, then tells
// the tracker something changed.
func (b *Backend) apply(f func() error) error {
	b.mu.Lock()
	err := f()
	b.mu.Unlock()
	b.signal()
	return err
}

// afterRegistrySync runs when every initial global has been handled and its
// binds sent; a second round trip then flushes the outputs' first events.
func (b *Backend) afterRegistrySync() error {
	cb := b.newID()
	b.handlers[cb] = func(uint16, *argReader) error {
		delete(b.handlers, cb)
		close(b.ready)
		return nil
	}
	return b.send(marshal(idDisplay, 0, cb))
}

// Displays returns the outputs that have reported complete state.
func (b *Backend) Displays() ([]contracts.Display, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return nil, b.err
	}
	var out []contracts.Display
	for _, o := range b.outputs {
		if o.ready(b.haveXDG) {
			out = append(out, o.display())
		}
	}
	slices.SortFunc(out, func(a, c contracts.Display) int {
		return strings.Compare(string(a.ID), string(c.ID))
	})
	markPrimary(out)
	return out, nil
}

// markPrimary picks the output at the logical origin, else the first.
func markPrimary(ds []contracts.Display) {
	pick := 0
	for i, d := range ds {
		if d.Bounds.Min == (contracts.Point{}) {
			pick = i
			break
		}
	}
	if len(ds) > 0 {
		ds[pick].Primary = true
	}
}
