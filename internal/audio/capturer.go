package audio

import (
	"context"
	"sync"
	"time"
)

// DefaultStallTimeout is how long a stream may go without any input before it
// is declared lost. Some backends (notably PulseAudio when a source vanishes)
// stop calling back without reporting an error; this keeps Read from hanging.
const DefaultStallTimeout = 2 * time.Second

// releaseTimeout bounds how long ending a stream waits for the backend to
// release the device.
const releaseTimeout = time.Second

// frameBuffer is how many frames may queue for a slow reader (1 s) before the
// pipeline applies backpressure to the raw stream.
const frameBuffer = 50

// Option configures a Capturer.
type Option func(*capturer)

// WithStallTimeout overrides DefaultStallTimeout.
func WithStallTimeout(d time.Duration) Option {
	return func(c *capturer) { c.stall = d }
}

type capturer struct {
	src   Source
	stall time.Duration
}

// NewCapturer wraps a platform Source with resampling to SampleRate mono,
// framing into FrameDuration and level metering.
func NewCapturer(src Source, opts ...Option) Capturer {
	c := &capturer{src: src, stall: DefaultStallTimeout}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *capturer) Devices(ctx context.Context) ([]Device, error) {
	return c.src.Devices(ctx)
}

func (c *capturer) Open(ctx context.Context, deviceID string) (Stream, error) {
	raw, err := c.src.OpenRaw(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	f := raw.Format()
	rs, err := NewResampler(f.SampleRate, SampleRate)
	if err != nil {
		raw.Close()
		return nil, err
	}
	s := &stream{
		raw:      raw,
		channels: max(f.Channels, 1),
		rs:       rs,
		stall:    c.stall,
		out:      make(chan Frame, frameBuffer),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		level:    Level{RMS: MinDBFS, Peak: MinDBFS},
	}
	go s.run(ctx)
	return s, nil
}

type stream struct {
	raw      RawStream
	channels int
	rs       *Resampler
	stall    time.Duration

	out      chan Frame
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}

	mu    sync.Mutex
	level Level
	err   error

	// pipeline state, owned by run
	mono    []float32
	pending []float64
	emitted int64 // samples delivered so far, for Frame.At
}

func (s *stream) Frames() <-chan Frame { return s.out }

func (s *stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *stream) Level() Level {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.level
}

func (s *stream) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
	return nil
}

// run owns the raw stream: it is the only goroutine that reads it and the one
// that releases it, so the device is normally freed before Frames is closed.
func (s *stream) run(ctx context.Context) {
	defer close(s.done)
	defer close(s.out)
	err := s.pump(ctx)
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	// A native backend that wedges in release must not wedge the session
	// too: after releaseTimeout we give up waiting and leave the release
	// goroutine behind, which beats a hung voice session.
	released := make(chan struct{})
	go func() {
		defer close(released)
		s.raw.Close()
	}()
	t := time.NewTimer(releaseTimeout)
	defer t.Stop()
	select {
	case <-released:
	case <-t.C:
	}
}

func (s *stream) pump(ctx context.Context) error {
	stall := time.NewTimer(s.stall)
	defer stall.Stop()
	chunks := s.raw.Chunks()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stop:
			return nil
		case <-stall.C:
			return ErrDeviceLost
		case chunk, ok := <-chunks:
			if !ok {
				return s.raw.Err()
			}
			if err := s.process(ctx, chunk); err != nil {
				return err
			}
			// Reset after processing: time spent blocked on a slow reader
			// is not a device stall.
			stall.Reset(s.stall)
		}
	}
}

// process converts one raw chunk and emits every complete frame. A blocked
// reader stalls only this goroutine; cancellation still gets through.
func (s *stream) process(ctx context.Context, chunk []float32) error {
	s.mono = Downmix(s.mono[:0], chunk, s.channels)
	s.pending = s.rs.Process(s.pending, s.mono)
	n := 0
	for ; n+FrameSamples <= len(s.pending); n += FrameSamples {
		f := Frame{Samples: make([]int16, FrameSamples), At: time.Duration(s.emitted) * time.Second / SampleRate}
		for i, v := range s.pending[n : n+FrameSamples] {
			f.Samples[i] = ToInt16(v)
		}
		s.emitted += int64(FrameSamples)
		lvl := MeasureLevel(f.Samples)
		select {
		case s.out <- f:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stop:
			return nil
		}
		s.mu.Lock()
		s.level = lvl
		s.mu.Unlock()
	}
	s.pending = append(s.pending[:0], s.pending[n:]...)
	return nil
}
