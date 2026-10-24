package stt

import (
	"context"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/stt/protocol"
)

// Partial is an interim transcript; each one replaces the previous.
type Partial struct {
	Seq    uint64
	Text   string
	Stable bool
}

// Endpoint reasons carried by EndpointEvent.
const (
	EndpointSilence     = protocol.EndpointSilence
	EndpointNoSpeech    = protocol.EndpointNoSpeech
	EndpointMaxDuration = protocol.EndpointMaxDuration
)

// EndpointEvent says hands-free capture should stop. The core normally
// reacts with Finish; the session stays valid until then.
type EndpointEvent struct {
	Reason string
	// At is the position in session audio of the decision.
	At time.Duration
}

type Segment struct {
	Start, End time.Duration
	Text       string
}

type Final struct {
	Text     string
	Language string
	Segments []Segment
	Duration time.Duration
	// DecodeTime is the sidecar's final decode wall time.
	DecodeTime time.Duration
	// Speech is false when the sidecar's VAD heard nothing; Text is empty.
	Speech bool
}

// maxChunk keeps audio lines small (1 s ≈ 43 KiB of base64) however much
// the caller writes at once.
const maxChunk = protocol.SampleRate

type Session struct {
	c         *Client
	id        string
	partials  chan Partial
	speech    chan bool
	endpoint  chan EndpointEvent
	cancelled chan struct{}

	// wmu orders audio chunks; it is separate from mu so a Write blocked on
	// a full stdin pipe never stalls the reader goroutine delivering events.
	wmu sync.Mutex
	seq uint64

	// mu also serialises channel sends with close in end, so delivery after
	// the session ended is a no-op rather than a panic.
	mu        sync.Mutex
	finishing bool
	ended     bool
	cancel    sync.Once
}

func (s *Session) ID() string { return s.id }

// Partials yields rolling transcripts and is closed when the session ends.
// If the consumer falls behind, older partials are dropped.
func (s *Session) Partials() <-chan Partial { return s.partials }

// Speech yields VAD transitions (true = speech started) and is closed when
// the session ends. Best effort: older events are dropped if not consumed.
func (s *Session) Speech() <-chan bool { return s.speech }

// Endpoint yields at most one event, only for sessions started with
// WithVAD, and is closed when the session ends. A partial transcript never
// implies an endpoint: only silence, a leading-silence timeout or the
// duration cap produce one.
func (s *Session) Endpoint() <-chan EndpointEvent { return s.endpoint }

// Write sends 16 kHz mono samples.
func (s *Session) Write(pcm []int16) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	closed := s.ended || s.finishing
	s.mu.Unlock()
	if closed {
		return ErrSessionClosed
	}
	for len(pcm) > 0 {
		n := min(len(pcm), maxChunk)
		m, err := protocol.NewNotification(protocol.MethodSessionAudio, protocol.SessionAudioParams{
			SessionID: s.id, Seq: s.seq, Data: protocol.EncodePCM16(pcm[:n]),
		})
		if err != nil {
			return err
		}
		if err := s.c.send(m); err != nil {
			return err
		}
		s.seq++
		pcm = pcm[n:]
	}
	return nil
}

// Finish ends audio input and returns the transcript of the whole
// utterance. If ctx ends first the sidecar decode is cancelled.
func (s *Session) Finish(ctx context.Context) (Final, error) {
	s.mu.Lock()
	if s.ended || s.finishing {
		s.mu.Unlock()
		select {
		case <-s.cancelled:
			return Final{}, ErrCancelled
		default:
			return Final{}, ErrSessionClosed
		}
	}
	s.finishing = true
	s.mu.Unlock()

	var res protocol.FinalResult
	err := s.c.call(ctx, protocol.MethodSessionFinish, protocol.SessionRef{SessionID: s.id}, &res, s.cancelled)
	if ctx.Err() != nil {
		s.Cancel()
		return Final{}, ctx.Err()
	}
	s.end()
	if err != nil {
		return Final{}, err
	}
	f := Final{
		Text:       res.Text,
		Language:   res.Language,
		Duration:   time.Duration(res.DurationMS) * time.Millisecond,
		DecodeTime: time.Duration(res.DecodeMS) * time.Millisecond,
		Speech:     res.Speech,
	}
	for _, seg := range res.Segments {
		f.Segments = append(f.Segments, Segment{
			Start: time.Duration(seg.StartMS) * time.Millisecond,
			End:   time.Duration(seg.EndMS) * time.Millisecond,
			Text:  seg.Text,
		})
	}
	return f, nil
}

// Cancel abandons the session without waiting for the sidecar. A pending
// Finish returns ErrCancelled; later events for the session are dropped.
func (s *Session) Cancel() {
	s.cancel.Do(func() {
		close(s.cancelled)
		s.mu.Lock()
		ended := s.ended
		s.mu.Unlock()
		s.end()
		if !ended {
			s.c.notifyRequest(protocol.MethodSessionCancel, protocol.SessionRef{SessionID: s.id})
		}
	})
}

func (s *Session) end() {
	s.c.mu.Lock()
	if s.c.sessions[s.id] == s {
		delete(s.c.sessions, s.id)
	}
	s.c.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	close(s.partials)
	close(s.speech)
	close(s.endpoint)
}

func (s *Session) deliverEndpoint(e EndpointEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || s.finishing {
		return
	}
	pushLatest(s.endpoint, e)
}

func (s *Session) deliverPartial(p Partial) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || s.finishing {
		return
	}
	pushLatest(s.partials, p)
}

func (s *Session) deliverSpeech(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || s.finishing {
		return
	}
	pushLatest(s.speech, v)
}

// pushLatest never blocks the reader goroutine: when the buffer is full the
// oldest value is discarded, since newer values supersede it.
func pushLatest[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}
