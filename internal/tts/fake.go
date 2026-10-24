package tts

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"sync"
	"time"
)

// Fake is a deterministic in-memory Provider for tests of other packages.
// Text length is ignored: it always emits Chunks chunks of ChunkBytes bytes
// of a 440 Hz sine, PCM16 at the requested rate, Delay apart.
type Fake struct {
	Voices     []Voice
	Chunks     int           // default 4
	ChunkBytes int           // default 4800 (100 ms of 24 kHz mono)
	Delay      time.Duration // pause before each chunk
	Err        error         // returned from SynthesizeStream if set

	b    base
	once sync.Once

	mu       sync.Mutex
	requests []SynthesisRequest
}

func (f *Fake) init() { f.once.Do(func() { f.b = newBase("fake", Config{}, nil) }) }

// Requests returns the normalized requests received so far.
func (f *Fake) Requests() []SynthesisRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SynthesisRequest(nil), f.requests...)
}

func (f *Fake) ListVoices(context.Context) ([]Voice, error) {
	if len(f.Voices) == 0 {
		return []Voice{{ID: "fake", Name: "Fake", Languages: []string{"en"}}}, nil
	}
	return append([]Voice(nil), f.Voices...), nil
}

func (f *Fake) Cancel(ctx context.Context) error { f.init(); return f.b.Cancel(ctx) }

func (f *Fake) SynthesizeStream(ctx context.Context, req SynthesisRequest) (Stream, error) {
	f.init()
	req, err := req.normalize()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	n, size := f.Chunks, f.ChunkBytes
	if n == 0 {
		n = 4
	}
	if size == 0 {
		size = 4800
	}
	rate := req.Format.SampleRate
	if rate == 0 {
		rate = 24000
	}
	ch := max(req.Format.Channels, 1)
	sctx, cancel := context.WithCancel(ctx)
	r := &fakeReader{ctx: sctx, left: n * size, delay: f.Delay, rate: rate, ch: ch, chunk: size}
	return newReaderStream(sctx, cancel, ctx, io.NopCloser(r), PCM16(rate, ch), "fake", f.b.track(cancel)), nil
}

type fakeReader struct {
	ctx   context.Context
	left  int
	delay time.Duration
	rate  int
	ch    int
	chunk int
	pos   int // sample index, keeps the waveform continuous
}

func (r *fakeReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	} else if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n := min(len(p), r.left, r.chunk)
	frame := 2 * r.ch
	n -= n % frame
	for i := 0; i < n; i += frame {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(r.pos)/float64(r.rate)))
		for c := 0; c < r.ch; c++ {
			binary.LittleEndian.PutUint16(p[i+2*c:], uint16(v))
		}
		r.pos++
	}
	r.left -= n
	return n, nil
}
