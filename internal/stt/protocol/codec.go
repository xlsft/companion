package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// MaxLineBytes bounds one framed message. Audio chunks are ~4 KiB of base64
// per 100 ms, so this leaves room for multi-second chunks without letting a
// corrupt stream grow memory unbounded.
const MaxLineBytes = 8 << 20

// Encoder writes newline-delimited messages. It is safe for concurrent use:
// each message is written and flushed as one unit so lines never interleave.
type Encoder struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: bufio.NewWriter(w)} }

func (e *Encoder) Encode(m *Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write(b); err != nil {
		return err
	}
	if err := e.w.WriteByte('\n'); err != nil {
		return err
	}
	return e.w.Flush()
}

// Decoder reads newline-delimited messages.
type Decoder struct {
	s *bufio.Scanner
}

func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64<<10), MaxLineBytes)
	return &Decoder{s: s}
}

// Decode returns the next message. A malformed line yields an *Error
// (ErrParse or ErrInvalidRequest) and the stream stays usable; io.EOF and
// transport errors are terminal.
func (d *Decoder) Decode() (*Message, error) {
	for d.s.Scan() {
		line := bytes.TrimSpace(d.s.Bytes())
		if len(line) == 0 {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, ErrParse
		}
		if m.JSONRPC != jsonrpcVersion || (m.Method == "" && m.ID == nil) {
			return &m, ErrInvalidRequest
		}
		return &m, nil
	}
	if err := d.s.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, ErrParse.WithDetail("line too long")
		}
		return nil, err
	}
	return nil, io.EOF
}

// IsTerminal reports whether a Decode error ends the stream.
func IsTerminal(err error) bool {
	var pe *Error
	return err != nil && (!errors.As(err, &pe) || pe.Data == "line too long")
}
