// Package wire holds the HTTP and server-sent-events plumbing shared by
// the LLM adapters.
package wire

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// MaxEventSize bounds one SSE event so a broken server cannot exhaust
// memory.
const MaxEventSize = 16 << 20

// SSEEvent is one dispatched server-sent event.
type SSEEvent struct {
	Event string // "event:" field; empty when absent
	Data  []byte // "data:" lines joined with '\n'
}

// SSEReader parses an event stream. Line endings may be LF or CRLF; bare
// CR (allowed by the spec, unused by LLM providers) is not supported.
type SSEReader struct {
	r *bufio.Reader
}

func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next event. At a clean end of input a pending event
// without the terminating blank line is still dispatched (several
// compatible servers omit it), then io.EOF is returned. Other read errors
// are returned as is.
func (s *SSEReader) Next() (SSEEvent, error) {
	var ev SSEEvent
	var data []byte
	lines := 0
	pending := false
	for {
		line, err := s.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) && pending {
				ev.Data = data
				return ev, nil
			}
			return SSEEvent{}, err
		}
		if len(line) == 0 {
			if pending {
				ev.Data = data
				return ev, nil
			}
			continue
		}
		if line[0] == ':' { // comment / keep-alive
			continue
		}
		field, value := line, []byte(nil)
		if i := bytes.IndexByte(line, ':'); i >= 0 {
			field, value = line[:i], line[i+1:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
		}
		switch string(field) {
		case "event":
			ev.Event = string(value)
			pending = true
		case "data":
			if lines > 0 {
				data = append(data, '\n')
			}
			lines++
			if len(data)+len(value) > MaxEventSize {
				return SSEEvent{}, Malformed("event too large")
			}
			data = append(data, value...)
			pending = true
		}
		// "id", "retry" and unknown fields are irrelevant for one-shot
		// LLM streams.
	}
}

// readLine returns one line without its terminator. The returned slice is
// valid until the next call.
func (s *SSEReader) readLine() ([]byte, error) {
	line, err := s.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		buf := append([]byte(nil), line...)
		for errors.Is(err, bufio.ErrBufferFull) {
			if len(buf) > MaxEventSize {
				return nil, Malformed("line too long")
			}
			line, err = s.r.ReadSlice('\n')
			buf = append(buf, line...)
		}
		line = buf
	}
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			// Final line without a terminator; report it now, EOF next time.
			return trimEOL(line), nil
		}
		return nil, err
	}
	return trimEOL(line), nil
}

func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}
