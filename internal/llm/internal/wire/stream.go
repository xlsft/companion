package wire

import (
	"errors"
	"io"
	"sync/atomic"

	"github.com/xlsft/openclick/internal/llm"
)

// Decoder converts provider SSE events into normalized events. It returns
// done once the provider's end-of-stream marker was seen; the events of
// that call must then end with llm.Completed. A non-nil err ends the
// stream with llm.Failed after the returned events.
type Decoder interface {
	Decode(ev SSEEvent) (events []llm.Event, done bool, err error)
}

// Stream implements llm.EventStream on top of a Body and a Decoder.
type Stream struct {
	body     *Body
	sse      *SSEReader
	dec      Decoder
	queue    []llm.Event
	finished bool
	closed   atomic.Bool
}

func NewStream(body *Body, dec Decoder) *Stream {
	return &Stream{body: body, sse: NewSSEReader(body), dec: dec}
}

// Recv returns the next event; see llm.EventStream. After Close it
// returns io.EOF.
func (s *Stream) Recv() (llm.Event, error) {
	for len(s.queue) == 0 {
		if s.finished || s.closed.Load() {
			return nil, io.EOF
		}
		ev, err := s.sse.Next()
		if err != nil {
			if s.closed.Load() {
				return nil, io.EOF
			}
			if errors.Is(err, io.EOF) {
				err = Malformed("stream ended before completion")
			}
			s.fail(err)
			continue
		}
		events, done, err := s.dec.Decode(ev)
		s.queue = append(s.queue, events...)
		switch {
		case err != nil:
			s.fail(err)
		case done:
			s.finish()
		}
	}
	ev := s.queue[0]
	s.queue = s.queue[1:]
	return ev, nil
}

func (s *Stream) fail(err error) {
	s.queue = append(s.queue, llm.Failed{Err: err})
	s.finish()
}

func (s *Stream) finish() {
	s.finished = true
	s.body.Close()
}

// Close may be called concurrently with Recv to abort it.
func (s *Stream) Close() error {
	s.closed.Store(true)
	return s.body.Close()
}
