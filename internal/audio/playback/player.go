package playback

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xlsft/openclick/internal/audio"
)

const (
	// DefaultPreRoll is how much audio is buffered before playback starts:
	// enough to ride out typical chunk jitter of a streaming TTS response
	// without adding noticeably to time to first audio.
	DefaultPreRoll = 120 * time.Millisecond
	// DefaultBuffer bounds how far the source may run ahead of the device;
	// beyond it Play stops reading and the HTTP stream backs off.
	DefaultBuffer = 2 * time.Second
	// DefaultStallTimeout is how long the device may stop pulling audio
	// before the playback ends with ErrDeviceLost. PulseAudio stops calling
	// back without an error when a sink vanishes; this keeps Wait from
	// hanging.
	DefaultStallTimeout = 2 * time.Second
	// releaseTimeout bounds how long ending a playback waits for the
	// backend to release the device.
	releaseTimeout = time.Second
)

// Option configures a Player.
type Option func(*player)

// WithDevice selects the output device by ID; "" (default) follows the
// system default.
func WithDevice(id string) Option { return func(p *player) { p.device = id } }

// WithPreRoll overrides DefaultPreRoll. Playback also starts as soon as the
// source ends, so short clips never wait for it.
func WithPreRoll(d time.Duration) Option { return func(p *player) { p.preRoll = d } }

// WithBuffer overrides DefaultBuffer.
func WithBuffer(d time.Duration) Option { return func(p *player) { p.buffer = d } }

// WithStallTimeout overrides DefaultStallTimeout; 0 disables the watchdog.
func WithStallTimeout(d time.Duration) Option { return func(p *player) { p.stall = d } }

type player struct {
	sink    Sink
	device  string
	preRoll time.Duration
	buffer  time.Duration
	stall   time.Duration
}

// New returns a Player that opens a device from sink for each playback.
func New(sink Sink, opts ...Option) Player {
	p := &player{sink: sink, preRoll: DefaultPreRoll, buffer: DefaultBuffer, stall: DefaultStallTimeout}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *player) Play(ctx context.Context, src ChunkSource, f Format) (Playback, error) {
	if f.SampleRate <= 0 || f.Channels <= 0 {
		src.Close()
		return nil, fmt.Errorf("playback: invalid source format %d Hz, %d ch", f.SampleRate, f.Channels)
	}
	if err := ctx.Err(); err != nil {
		src.Close()
		return nil, err
	}
	out, err := p.sink.OpenOutput(ctx, p.device)
	if err != nil {
		src.Close()
		return nil, err
	}
	df := out.Format()
	fail := func(err error) (Playback, error) {
		out.Close()
		src.Close()
		return nil, err
	}
	if df.SampleRate <= 0 || df.Channels <= 0 {
		return fail(fmt.Errorf("playback: device reports format %d Hz, %d ch", df.SampleRate, df.Channels))
	}
	var rs *audio.Resampler
	if df.SampleRate != f.SampleRate {
		if rs, err = audio.NewResampler(f.SampleRate, df.SampleRate); err != nil {
			return fail(err)
		}
	}
	frames := func(d time.Duration) int { return max(int(d.Seconds()*float64(df.SampleRate)), 1) }
	preroll := frames(p.preRoll)
	pctx, cancel := context.WithCancel(ctx)
	pb := &playback{
		src:          src,
		out:          out,
		in:           f,
		dev:          df,
		rs:           rs,
		stall:        p.stall,
		cancel:       cancel,
		preroll:      preroll * df.Channels,
		ring:         newRing(max(frames(p.buffer), 2*preroll) * df.Channels),
		space:        make(chan struct{}, 1),
		finished:     make(chan struct{}),
		started:      make(chan struct{}),
		stopReq:      make(chan struct{}),
		done:         make(chan struct{}),
		producerDone: make(chan struct{}),
	}
	if err := out.Start(pb.render); err != nil {
		cancel()
		return fail(err)
	}
	go pb.produce(pctx)
	go pb.supervise(ctx)
	return pb, nil
}

type state int

const (
	stateBuffering state = iota // pre-roll, or refilling after an underrun
	statePlaying
	stateDrained // source ended and every sample was rendered
	stateStopped
)

type playback struct {
	src    ChunkSource
	out    Output
	in     Format
	dev    Format
	rs     *audio.Resampler
	stall  time.Duration
	cancel context.CancelFunc

	// renders counts render calls, for the stall watchdog.
	renders atomic.Uint64

	// mu is taken by the audio thread; hold it only for buffer bookkeeping.
	mu        sync.Mutex
	ring      ring
	preroll   int // samples (frames × device channels)
	state     state
	eof       bool
	srcErr    error
	underruns int
	played    int64 // device frames of source audio rendered
	isStarted bool

	space        chan struct{} // render freed buffer space
	finished     chan struct{} // closed on stateDrained
	started      chan struct{}
	stopReq      chan struct{}
	stopOnce     sync.Once
	done         chan struct{}
	producerDone chan struct{}
	err          error // set before done is closed
}

func (p *playback) Started() <-chan struct{} { return p.started }
func (p *playback) Done() <-chan struct{}    { return p.done }

func (p *playback) Wait() error {
	<-p.done
	return p.err
}

func (p *playback) Stop() {
	p.stopOnce.Do(func() { close(p.stopReq) })
	<-p.done
}

func (p *playback) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{Underruns: p.underruns, Played: time.Duration(p.played) * time.Second / time.Duration(p.dev.SampleRate)}
}

// render runs on the device's audio thread: no allocation, no blocking
// beyond the short critical section.
func (p *playback) render(out []float32) {
	p.renders.Add(1)
	n := 0
	p.mu.Lock()
	if p.state == stateBuffering {
		switch {
		case p.ring.n >= p.preroll || (p.eof && p.ring.n > 0):
			p.state = statePlaying
		case p.eof:
			p.drain()
		}
	}
	if p.state == statePlaying {
		n = p.ring.read(out)
		if n > 0 {
			p.played += int64(n / p.dev.Channels)
			if !p.isStarted {
				p.isStarted = true
				close(p.started)
			}
		}
		if n < len(out) {
			if p.eof {
				p.drain()
			} else {
				// Refill to the pre-roll instead of playing every chunk
				// the moment it lands, which would stutter on a slow link.
				p.underruns++
				p.state = stateBuffering
			}
		}
	}
	p.mu.Unlock()
	clear(out[n:])
	select {
	case p.space <- struct{}{}:
	default:
	}
}

// drain must be called with mu held.
func (p *playback) drain() {
	p.state = stateDrained
	close(p.finished)
}

// produce reads, converts and buffers the source until it ends or ctx is
// cancelled.
func (p *playback) produce(ctx context.Context) {
	defer close(p.producerDone)
	var (
		carry []byte
		mono  []float32
		res   []float64
		out   []float32
	)
	frameBytes := 2 * p.in.Channels
	for {
		data, err := p.src.Next(ctx)
		if len(data) > 0 {
			if len(carry) > 0 {
				data = append(carry, data...)
				carry = nil
			}
			if rem := len(data) % frameBytes; rem != 0 {
				carry = append([]byte(nil), data[len(data)-rem:]...)
				data = data[:len(data)-rem]
			}
			mono = decode(mono[:0], data, p.in.Channels)
			if res, out = p.convert(res[:0], out[:0], mono, false); !p.write(ctx, out) {
				return
			}
		}
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return // hard cancel: the supervisor reports why
		}
		if errors.Is(err, io.EOF) {
			err = nil
		}
		// A broken stream still plays what arrived before it broke.
		if _, out = p.convert(res[:0], out[:0], nil, true); !p.write(ctx, out) {
			return
		}
		p.mu.Lock()
		p.eof, p.srcErr = true, err
		p.mu.Unlock()
		return
	}
}

// decode turns interleaved PCM16 into mono float32.
func decode(dst []float32, data []byte, channels int) []float32 {
	inv := 1 / (32768 * float32(channels))
	for i := 0; i+2*channels <= len(data); i += 2 * channels {
		var s float32
		for c := range channels {
			s += float32(int16(binary.LittleEndian.Uint16(data[i+2*c:])))
		}
		dst = append(dst, s*inv)
	}
	return dst
}

// convert resamples mono to the device rate and spreads it over the device
// channels. Speech is mono, so every channel carries the same signal.
func (p *playback) convert(res []float64, out []float32, mono []float32, flush bool) ([]float64, []float32) {
	ch := p.dev.Channels
	emit := func(v float32) {
		for range ch {
			out = append(out, v)
		}
	}
	if p.rs == nil {
		for _, v := range mono {
			emit(v)
		}
		return res, out
	}
	if flush {
		res = p.rs.Flush(res)
	} else {
		res = p.rs.Process(res, mono)
	}
	for _, v := range res {
		emit(float32(min(max(v, -1), 1)))
	}
	return res, out
}

// write blocks until all samples are buffered; false means the playback is
// over.
func (p *playback) write(ctx context.Context, samples []float32) bool {
	for {
		p.mu.Lock()
		if p.state == stateStopped {
			p.mu.Unlock()
			return false
		}
		k := p.ring.write(samples)
		p.mu.Unlock()
		if samples = samples[k:]; len(samples) == 0 {
			return true
		}
		select {
		case <-p.space:
		case <-ctx.Done():
			return false
		}
	}
}

func (p *playback) supervise(ctx context.Context) {
	var stallC <-chan time.Time
	if p.stall > 0 {
		t := time.NewTicker(p.stall / 4)
		defer t.Stop()
		stallC = t.C
	}
	last, lastAt := p.renders.Load(), time.Now()
	lost := p.out.Lost()
	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			break loop
		case <-p.stopReq:
			err = ErrStopped
			break loop
		case <-lost:
			err = ErrDeviceLost
			break loop
		case <-p.finished:
			// Every sample is with the device; let its buffers play out.
			t := time.NewTimer(p.out.Latency())
			select {
			case <-t.C:
				p.mu.Lock()
				err = p.srcErr
				p.mu.Unlock()
			case <-ctx.Done():
				err = ctx.Err()
			case <-p.stopReq:
				err = ErrStopped
			case <-lost:
				err = ErrDeviceLost
			}
			t.Stop()
			break loop
		case now := <-stallC:
			if r := p.renders.Load(); r != last {
				last, lastAt = r, now
			} else if now.Sub(lastAt) >= p.stall {
				err = ErrDeviceLost
				break loop
			}
		}
	}
	p.shutdown(err)
}

func (p *playback) shutdown(err error) {
	// Silence first: the next render outputs zeros whatever the backend
	// does next, which is what bounds the stop latency to one period.
	p.mu.Lock()
	p.state = stateStopped
	p.ring.reset()
	p.mu.Unlock()
	p.cancel()

	// A native backend that wedges in release must not wedge the session:
	// after releaseTimeout we stop waiting and leave the goroutine behind.
	released := make(chan struct{})
	go func() {
		defer close(released)
		p.out.Close()
	}()
	<-p.producerDone // Next and write both honour the cancelled ctx
	p.src.Close()
	t := time.NewTimer(releaseTimeout)
	select {
	case <-released:
	case <-t.C:
	}
	t.Stop()
	p.err = err
	close(p.done)
}

// ring is a fixed-capacity FIFO of samples. Callers keep reads and writes in
// whole frames, so n is always a multiple of the channel count.
type ring struct {
	buf  []float32
	r, n int
}

func newRing(capacity int) ring { return ring{buf: make([]float32, capacity)} }

func (q *ring) write(p []float32) int {
	k := min(len(p), len(q.buf)-q.n)
	w := (q.r + q.n) % len(q.buf)
	c := copy(q.buf[w:], p[:k])
	copy(q.buf, p[c:k])
	q.n += k
	return k
}

func (q *ring) read(p []float32) int {
	k := min(len(p), q.n)
	c := copy(p[:k], q.buf[q.r:])
	copy(p[c:k], q.buf)
	q.r = (q.r + k) % len(q.buf)
	q.n -= k
	return k
}

func (q *ring) reset() { q.r, q.n = 0, 0 }
