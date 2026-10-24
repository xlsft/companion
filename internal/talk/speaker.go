package talk

import (
	"context"
	"errors"
	"sync"

	"github.com/xlsft/openclick/internal/audio/playback"
	"github.com/xlsft/openclick/internal/tts"
)

// Prefetcher is an optional Speaker extension. Prefetch starts producing the
// audio for text before its Say, so the next segment is already streaming
// while the current one plays and consecutive segments hand off without a
// synthesis round trip of silence in between. Prefetches may be issued before
// or during the Say of the segment ahead of them; ctx ending discards them.
type Prefetcher interface {
	Prefetch(ctx context.Context, text string)
}

// TTSSpeaker speaks through a streaming TTS provider and a Player
// (FR-VOICE-004..006). Audio plays as it arrives; cancelling the ctx given to
// Say aborts the HTTP stream and silences the device at once.
//
// Each Say opens the output device anew (about 10 ms on PipeWire); with
// Prefetch the next segment's first audio is usually buffered by then, so
// the gap between segments is the device open plus one period, not a
// synthesis round trip.
type TTSSpeaker struct {
	Provider tts.Provider
	Player   playback.Player
	Voice    string
	// Speed returns the speech rate, clamped to [tts.MinSpeed,
	// tts.MaxSpeed]; providers apply it during synthesis (FR-VOICE-010). It
	// is read per segment so a settings change applies to the next one. Nil
	// or 0 means tts.DefaultSpeed.
	Speed func() float64
	// Format is requested from the provider; zero means tts.PCM24kMono.
	// It must be PCM16.
	Format tts.Format

	mu  sync.Mutex
	pre []*prefetch // oldest first, at most maxPrefetch
}

// maxPrefetch bounds the open synthesis requests kept for segments not yet
// said: the next one, plus one that raced ahead of the current Say.
const maxPrefetch = 2

var (
	_ Speaker    = (*TTSSpeaker)(nil)
	_ Prefetcher = (*TTSSpeaker)(nil)
)

var errSpeakerUnconfigured = errors.New("talk: TTSSpeaker needs a Provider and a Player")

type prefetch struct {
	text   string
	cancel context.CancelFunc
	ready  chan struct{}
	stream tts.Stream // valid after ready
	err    error
}

// discard aborts the prefetch and closes its stream once it exists.
func (p *prefetch) discard() {
	if p == nil {
		return
	}
	p.cancel()
	go func() {
		<-p.ready
		if p.stream != nil {
			p.stream.Close()
		}
	}()
}

func (s *TTSSpeaker) format() tts.Format {
	if s.Format == (tts.Format{}) {
		return tts.PCM24kMono
	}
	return s.Format
}

func (s *TTSSpeaker) request(text string) tts.SynthesisRequest {
	speed := tts.DefaultSpeed
	if s.Speed != nil {
		switch v := s.Speed(); {
		case v == 0 || v != v: // unset or NaN
		case v < tts.MinSpeed:
			speed = tts.MinSpeed
		case v > tts.MaxSpeed:
			speed = tts.MaxSpeed
		default:
			speed = v
		}
	}
	return tts.SynthesisRequest{Text: text, VoiceID: s.Voice, Speed: speed, Format: s.format()}
}

func (s *TTSSpeaker) Prefetch(ctx context.Context, text string) {
	if s.Provider == nil {
		return
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &prefetch{text: text, cancel: cancel, ready: make(chan struct{})}
	req := s.request(text)
	s.mu.Lock()
	var evicted *prefetch
	if len(s.pre) == maxPrefetch {
		evicted, s.pre = s.pre[0], s.pre[1:]
	}
	s.pre = append(s.pre, p)
	s.mu.Unlock()
	evicted.discard()
	go func() {
		defer close(p.ready)
		p.stream, p.err = s.Provider.SynthesizeStream(pctx, req)
	}()
}

// Say synthesises text, plays it and returns when playback ends. started is
// called once, when the first audio reaches the device; it is not called if
// nothing was played.
func (s *TTSSpeaker) Say(ctx context.Context, text string, started func()) error {
	if s.Provider == nil || s.Player == nil {
		return errSpeakerUnconfigured
	}
	stream, release, err := s.open(ctx, text)
	if err != nil {
		return err
	}
	defer release()
	src, f, err := playback.FromTTS(stream, s.format())
	if err != nil {
		return err
	}
	pb, err := s.Player.Play(ctx, src, f)
	if err != nil {
		return err
	}
	select {
	case <-pb.Started():
	case <-pb.Done():
	}
	select {
	case <-pb.Started():
		if started != nil {
			started()
		}
	default:
	}
	return pb.Wait()
}

// open returns the stream for text, taking over a matching prefetch.
// Prefetches older than the match were skipped and are discarded; newer ones
// are for later segments and stay. release must be called once the stream is
// no longer used.
func (s *TTSSpeaker) open(ctx context.Context, text string) (tts.Stream, func(), error) {
	var p *prefetch
	var skipped []*prefetch
	s.mu.Lock()
	for i, q := range s.pre {
		if q.text == text {
			p, skipped = q, s.pre[:i:i]
			s.pre = append([]*prefetch(nil), s.pre[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	for _, q := range skipped {
		q.discard()
	}
	if p == nil {
		st, err := s.Provider.SynthesizeStream(ctx, s.request(text))
		return st, func() {}, err
	}
	// The prefetch runs under its own ctx; from now on Say's ctx governs it.
	stop := context.AfterFunc(ctx, p.cancel)
	release := func() {
		stop()
		p.cancel()
	}
	select {
	case <-p.ready:
	case <-ctx.Done():
		release()
		p.discard()
		return nil, nil, ctx.Err()
	}
	if p.err != nil {
		release()
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, p.err
	}
	return p.stream, release, nil
}
