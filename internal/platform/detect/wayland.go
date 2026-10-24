package detect

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
)

// Global is one wl_registry.global advertisement.
type Global struct {
	Name      uint32
	Interface string
	Version   uint32
}

// Minimal Wayland wire client: just enough to list registry globals. Object
// ids are fixed: 1 = wl_display, 2 = wl_registry, 3 = wl_callback (sync).
const (
	idDisplay  = 1
	idRegistry = 2
	idCallback = 3

	opDisplaySync        = 0 // request
	opDisplayGetRegistry = 1 // request
	opDisplayError       = 0 // event
	opRegistryGlobal     = 0 // event
	opCallbackDone       = 0 // event
)

// The Wayland wire format uses host byte order.
var order = binary.NativeEndian

func appendMsg(b []byte, obj uint32, opcode uint16, args ...uint32) []byte {
	size := 8 + 4*len(args)
	b = order.AppendUint32(b, obj)
	b = order.AppendUint32(b, uint32(size)<<16|uint32(opcode))
	for _, a := range args {
		b = order.AppendUint32(b, a)
	}
	return b
}

// requestRegistry builds the get_registry + sync request pair.
func requestRegistry() []byte {
	b := appendMsg(nil, idDisplay, opDisplayGetRegistry, idRegistry)
	return appendMsg(b, idDisplay, opDisplaySync, idCallback)
}

// parseRegistry reads events until the sync callback fires and returns the
// globals seen before it.
func parseRegistry(r io.Reader) ([]Global, error) {
	var globals []Global
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			return globals, fmt.Errorf("wayland: read header: %w", err)
		}
		obj := order.Uint32(hdr[0:])
		w := order.Uint32(hdr[4:])
		size, op := int(w>>16), uint16(w&0xffff)
		if size < 8 || size > 4096 {
			return globals, fmt.Errorf("wayland: bad message size %d", size)
		}
		body := make([]byte, size-8)
		if _, err := io.ReadFull(r, body); err != nil {
			return globals, fmt.Errorf("wayland: read body: %w", err)
		}
		switch {
		case obj == idCallback && op == opCallbackDone:
			return globals, nil
		case obj == idDisplay && op == opDisplayError:
			return globals, errors.New("wayland: display error")
		case obj == idRegistry && op == opRegistryGlobal:
			g, err := parseGlobal(body)
			if err != nil {
				return globals, err
			}
			globals = append(globals, g)
		}
	}
}

func parseGlobal(body []byte) (Global, error) {
	if len(body) < 12 {
		return Global{}, errors.New("wayland: short global event")
	}
	name := order.Uint32(body)
	n := int(order.Uint32(body[4:])) // string length including the NUL
	padded := (n + 3) &^ 3
	if n < 1 || 8+padded+4 > len(body) {
		return Global{}, errors.New("wayland: bad global string")
	}
	iface := string(bytes.TrimRight(body[8:8+n], "\x00"))
	return Global{Name: name, Interface: iface, Version: order.Uint32(body[8+padded:])}, nil
}

// waylandSocketPath resolves WAYLAND_DISPLAY like libwayland does.
func waylandSocketPath(display, runtimeDir string) string {
	if filepath.IsAbs(display) {
		return display
	}
	return filepath.Join(runtimeDir, display)
}

// listGlobals connects to the compositor and returns the registry globals.
func listGlobals(ctx context.Context, path string) ([]Global, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("wayland: connect: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := conn.Write(requestRegistry()); err != nil {
		return nil, fmt.Errorf("wayland: write: %w", err)
	}
	return parseRegistry(conn)
}
