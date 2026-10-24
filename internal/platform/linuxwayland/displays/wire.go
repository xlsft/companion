package displays

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Wayland wire format helpers. Messages are an 8-byte header (object id,
// size<<16|opcode) followed by 32-bit aligned arguments in host byte order.
// This is a deliberately small cousin of the registry-only client in
// internal/platform/detect: here objects have dynamic ids and we need
// strings, signed ints and requests with new ids.

var order = binary.NativeEndian

// maxMessage guards against a corrupt stream; real events here are tiny.
const maxMessage = 1 << 16

// readMessage reads one event and returns its target object, opcode and body.
func readMessage(r io.Reader) (obj uint32, op uint16, body []byte, err error) {
	var hdr [8]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, 0, nil, err
	}
	obj = order.Uint32(hdr[0:])
	w := order.Uint32(hdr[4:])
	size := int(w >> 16)
	if size < 8 || size > maxMessage || size%4 != 0 {
		return 0, 0, nil, fmt.Errorf("wayland: bad message size %d", size)
	}
	body = make([]byte, size-8)
	if _, err = io.ReadFull(r, body); err != nil {
		return 0, 0, nil, err
	}
	return obj, uint16(w & 0xffff), body, nil
}

// marshal encodes a request. Args may be uint32, int32 or string.
func marshal(obj uint32, op uint16, args ...any) []byte {
	var body []byte
	for _, a := range args {
		switch v := a.(type) {
		case uint32:
			body = order.AppendUint32(body, v)
		case int32:
			body = order.AppendUint32(body, uint32(v))
		case string:
			body = order.AppendUint32(body, uint32(len(v)+1))
			body = append(body, v...)
			body = append(body, 0)
			for len(body)%4 != 0 {
				body = append(body, 0)
			}
		default:
			panic(fmt.Sprintf("wayland: cannot marshal %T", a))
		}
	}
	b := order.AppendUint32(nil, obj)
	b = order.AppendUint32(b, uint32(8+len(body))<<16|uint32(op))
	return append(b, body...)
}

var errShort = errors.New("wayland: truncated event")

// argReader decodes event arguments; the first failure sticks in err.
type argReader struct {
	b   []byte
	err error
}

func (r *argReader) u32() uint32 {
	if r.err != nil || len(r.b) < 4 {
		r.err = errShort
		return 0
	}
	v := order.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *argReader) i32() int32 { return int32(r.u32()) }

func (r *argReader) str() string {
	n := int(r.u32())
	if r.err != nil {
		return ""
	}
	padded := (n + 3) &^ 3
	if n < 0 || padded > len(r.b) {
		r.err = errShort
		return ""
	}
	if n == 0 { // null string
		return ""
	}
	s := string(r.b[:n-1]) // drop the NUL
	r.b = r.b[padded:]
	return s
}
