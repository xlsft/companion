package playback

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// FakeSink is a Sink with a simulated device clock, for tests. By default
// nothing renders until the test calls Tick on the output, so time is fully
// under test control; Realtime drives Tick from a wall-clock ticker instead.
type FakeSink struct {
	Format  Format        // default 48000 Hz, 2 channels
	Period  time.Duration // audio per Tick, default 10 ms
	Latency time.Duration // reported device latency, default Period
	// Realtime ticks every Period of wall time once started.
	Realtime bool
	// OpenErr, if set, is returned by OpenOutput.
	OpenErr error

	mu     sync.Mutex
	outs   []*FakeOutput
	active atomic.Int32
}

// Outputs returns every output opened so far, oldest first.
func (s *FakeSink) Outputs() []*FakeOutput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*FakeOutput(nil), s.outs...)
}

// Last returns the most recently opened output, or nil.
func (s *FakeSink) Last() *FakeOutput {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.outs) == 0 {
		return nil
	}
	return s.outs[len(s.outs)-1]
}

// Active is the number of outputs opened and not yet closed.
func (s *FakeSink) Active() int { return int(s.active.Load()) }

func (s *FakeSink) OpenOutput(ctx context.Context, _ string) (Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.OpenErr != nil {
		return nil, s.OpenErr
	}
	f := s.Format
	if f.SampleRate == 0 {
		f.SampleRate = 48000
	}
	if f.Channels == 0 {
		f.Channels = 2
	}
	period := s.Period
	if period <= 0 {
		period = 10 * time.Millisecond
	}
	latency := s.Latency
	if latency == 0 {
		latency = period
	}
	o := &FakeOutput{
		sink:    s,
		format:  f,
		period:  period,
		latency: latency,
		frames:  max(int(period.Seconds()*float64(f.SampleRate)), 1),
		lost:    make(chan struct{}),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	s.active.Add(1)
	s.mu.Lock()
	s.outs = append(s.outs, o)
	s.mu.Unlock()
	return o, nil
}

// FakeOutput is an Output opened from a FakeSink.
type FakeOutput struct {
	sink    *FakeSink
	format  Format
	period  time.Duration
	latency time.Duration
	frames  int

	// mu is held across render, so Close waits for an in-flight render the
	// way a real backend waits for its audio thread.
	mu       sync.Mutex
	render   func([]float32)
	rendered []float32
	ticks    int
	closed   bool
	hung     bool
	closedAt int // ticks rendered when Close was called

	lost     chan struct{}
	lostOnce sync.Once
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	running  bool
}

func (o *FakeOutput) Format() Format         { return o.format }
func (o *FakeOutput) Latency() time.Duration { return o.latency }
func (o *FakeOutput) Lost() <-chan struct{}  { return o.lost }
func (o *FakeOutput) Period() time.Duration  { return o.period }
func (o *FakeOutput) PeriodSamples() int     { return o.frames * o.format.Channels }

func (o *FakeOutput) Start(render func([]float32)) error {
	o.mu.Lock()
	o.render = render
	o.running = o.sink.Realtime
	o.mu.Unlock()
	if !o.running {
		close(o.done)
		return nil
	}
	go func() {
		defer close(o.done)
		t := time.NewTicker(o.period)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				o.Tick()
			case <-o.stop:
				return
			}
		}
	}()
	return nil
}

// Tick renders one period and reports whether the device was running.
func (o *FakeOutput) Tick() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.render == nil || o.closed || o.hung {
		return false
	}
	buf := make([]float32, o.frames*o.format.Channels)
	o.render(buf)
	o.rendered = append(o.rendered, buf...)
	o.ticks++
	return true
}

// Ticks is the number of periods rendered: the device clock.
func (o *FakeOutput) Ticks() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ticks
}

// Rendered returns a copy of every sample rendered so far.
func (o *FakeOutput) Rendered() []float32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]float32(nil), o.rendered...)
}

// Closed reports whether Close was called, and at which tick.
func (o *FakeOutput) Closed() (bool, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closed, o.closedAt
}

// Lose simulates an unplug: rendering stops and Lost is closed.
func (o *FakeOutput) Lose() {
	o.Hang()
	o.lostOnce.Do(func() { close(o.lost) })
}

// Hang stops rendering without any notification, like a backend that
// silently stops calling back.
func (o *FakeOutput) Hang() {
	o.mu.Lock()
	o.hung = true
	o.mu.Unlock()
}

func (o *FakeOutput) Close() error {
	o.mu.Lock()
	first := !o.closed
	if first {
		o.closed, o.closedAt = true, o.ticks
	}
	started := o.render != nil
	o.mu.Unlock()
	o.stopOnce.Do(func() { close(o.stop) })
	if started {
		<-o.done
	}
	if first {
		o.sink.active.Add(-1)
	}
	return nil
}

// SliceSource is a ChunkSource over fixed chunks, for tests.
type SliceSource struct {
	Chunks [][]byte
	// Delay is waited before each chunk, honouring ctx.
	Delay time.Duration
	// Err is returned after the chunks instead of io.EOF.
	Err error
	// Hold blocks after the chunks until ctx ends, like a stalled stream.
	Hold bool

	i      int
	closed atomic.Bool
}

// IsClosed reports whether Close was called.
func (s *SliceSource) IsClosed() bool { return s.closed.Load() }

func (s *SliceSource) Next(ctx context.Context) ([]byte, error) {
	if s.i >= len(s.Chunks) {
		if s.Hold {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if s.Err != nil {
			return nil, s.Err
		}
		return nil, io.EOF
	}
	if s.Delay > 0 {
		t := time.NewTimer(s.Delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := s.Chunks[s.i]
	s.i++
	return c, nil
}

func (s *SliceSource) Close() error {
	s.closed.Store(true)
	return nil
}

// FakePlayer is a Player without a device, for tests of packages that play
// audio. It reads each source to the end, optionally at real-time pace.
type FakePlayer struct {
	// Realtime paces reading at the audio's duration, so Stop and ctx
	// cancellation can land mid-playback.
	Realtime bool
	// Err, if set, is returned by Play.
	Err error

	mu     sync.Mutex
	plays  []FakePlay
	active atomic.Int32
}

// FakePlay records one finished playback.
type FakePlay struct {
	Format Format
	Bytes  int
	Err    error
}

// Plays returns the finished playbacks, in the order they ended.
func (f *FakePlayer) Plays() []FakePlay {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakePlay(nil), f.plays...)
}

// Active is the number of playbacks not yet ended.
func (f *FakePlayer) Active() int { return int(f.active.Load()) }

func (f *FakePlayer) Play(ctx context.Context, src ChunkSource, fm Format) (Playback, error) {
	if f.Err != nil {
		src.Close()
		return nil, f.Err
	}
	if err := ctx.Err(); err != nil {
		src.Close()
		return nil, err
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &fakePlayback{started: make(chan struct{}), done: make(chan struct{}), stop: make(chan struct{})}
	f.active.Add(1)
	go func() {
		defer f.active.Add(-1)
		go func() {
			select {
			case <-p.stop:
				cancel()
			case <-pctx.Done():
			}
		}()
		n, err := p.run(pctx, src, fm, f.Realtime)
		select {
		case <-p.stop:
			err = ErrStopped
		default:
			if err == nil && ctx.Err() != nil {
				err = ctx.Err()
			}
		}
		cancel()
		src.Close()
		f.mu.Lock()
		f.plays = append(f.plays, FakePlay{Format: fm, Bytes: n, Err: err})
		f.mu.Unlock()
		p.err = err
		close(p.done)
	}()
	return p, nil
}

type fakePlayback struct {
	started  chan struct{}
	done     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	err      error
}

func (p *fakePlayback) run(ctx context.Context, src ChunkSource, fm Format, realtime bool) (int, error) {
	total := 0
	bytesPerSec := 2 * fm.Channels * fm.SampleRate
	for {
		data, err := src.Next(ctx)
		if len(data) > 0 {
			if total == 0 {
				close(p.started)
			}
			total += len(data)
			if realtime && bytesPerSec > 0 {
				t := time.NewTimer(time.Duration(len(data)) * time.Second / time.Duration(bytesPerSec))
				select {
				case <-t.C:
				case <-ctx.Done():
					t.Stop()
					return total, ctx.Err()
				}
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

func (p *fakePlayback) Started() <-chan struct{} { return p.started }
func (p *fakePlayback) Done() <-chan struct{}    { return p.done }
func (p *fakePlayback) Wait() error              { <-p.done; return p.err }
func (p *fakePlayback) Stats() Stats             { return Stats{} }

func (p *fakePlayback) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	<-p.done
}
