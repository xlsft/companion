package wl

import (
	"encoding/binary"
	"math"
)

// Msg builds the argument payload of a request.
type Msg struct {
	b   []byte
	fds []int
}

func NewMsg() *Msg { return &Msg{} }

func (m *Msg) Uint(v uint32) *Msg {
	m.b = binary.LittleEndian.AppendUint32(m.b, v)
	return m
}

func (m *Msg) Int(v int32) *Msg { return m.Uint(uint32(v)) }

// Object appends an object id; 0 means null.
func (m *Msg) Object(id uint32) *Msg { return m.Uint(id) }

func (m *Msg) NewID(id uint32) *Msg { return m.Uint(id) }

// Fixed appends a 24.8 fixed-point number.
func (m *Msg) Fixed(v float64) *Msg { return m.Int(int32(math.Round(v * 256))) }

func (m *Msg) String(s string) *Msg {
	n := len(s) + 1
	m.Uint(uint32(n))
	m.b = append(m.b, s...)
	m.b = append(m.b, 0)
	for len(m.b)%4 != 0 {
		m.b = append(m.b, 0)
	}
	return m
}

// Fd attaches a file descriptor; it takes no space in the payload.
func (m *Msg) Fd(fd int) *Msg {
	m.fds = append(m.fds, fd)
	return m
}

// Event decodes the arguments of one event in order. Reading past the end
// yields zero values; handlers validate what they need.
type Event struct {
	b []byte
}

func (e *Event) Uint() uint32 {
	if len(e.b) < 4 {
		e.b = nil
		return 0
	}
	v := binary.LittleEndian.Uint32(e.b)
	e.b = e.b[4:]
	return v
}

func (e *Event) Int() int32 { return int32(e.Uint()) }

func (e *Event) Fixed() float64 { return float64(e.Int()) / 256 }

func (e *Event) String() string {
	n := int(e.Uint())
	if n == 0 || n > len(e.b) {
		e.b = nil
		return ""
	}
	s := string(e.b[:n-1])
	padded := (n + 3) &^ 3
	if padded > len(e.b) {
		padded = len(e.b)
	}
	e.b = e.b[padded:]
	return s
}

// Array returns the raw bytes of an array argument.
func (e *Event) Array() []byte {
	n := int(e.Uint())
	if n > len(e.b) {
		e.b = nil
		return nil
	}
	a := append([]byte(nil), e.b[:n]...)
	padded := (n + 3) &^ 3
	if padded > len(e.b) {
		padded = len(e.b)
	}
	e.b = e.b[padded:]
	return a
}
