package playback

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/tts"
)

const srcRate = 24000

var mono24k = Format{SampleRate: srcRate, Channels: 1}

// tone is d of a 1 kHz sine as mono PCM16 at srcRate, starting at sample
// offset off so consecutive chunks join without a discontinuity.
func tone(d time.Duration, off int) []byte {
	n := int(d.Seconds() * srcRate)
	b := make([]byte, 2*n)
	for i := range n {
		v := int16(8000 * math.Sin(2*math.Pi*1000*float64(off+i)/srcRate))
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// chanSource delivers what the test pushes; closing ch is end of stream.
type chanSource struct {
	ch     chan []byte
	closed atomic.Bool
}

func newChanSource() *chanSource { return &chanSource{ch: make(chan []byte, 64)} }

func (s *chanSource) Next(ctx context.Context) ([]byte, error) {
	select {
	case b, ok := <-s.ch:
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *chanSource) Close() error { s.closed.Store(true); return nil }

// noLeaks fails the test if goroutines started during it outlive it.
func noLeaks(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for runtime.NumGoroutine() > before {
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<16)
				t.Errorf("goroutines leaked: %d before, %d after\n%s", before, runtime.NumGoroutine(), buf[:runtime.Stack(buf, true)])
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func buffered(pb Playback) int {
	p := pb.(*playback)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ring.n
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func audible(samples []float32) int {
	n := 0
	for _, v := range samples {
		if v != 0 {
			n++
		}
	}
	return n
}

func play(t *testing.T, sink *FakeSink, src ChunkSource, opts ...Option) (Playback, *FakeOutput) {
	t.Helper()
	pb, err := New(sink, opts...).Play(context.Background(), src, mono24k)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pb.Stop)
	return pb, sink.Last()
}

func TestPreRollHoldsPlaybackUntilBuffered(t *testing.T) {
	noLeaks(t)
	sink := &FakeSink{}
	src := newChanSource()
	pb, out := play(t, sink, src)
	period := out.PeriodSamples()

	src.ch <- tone(40*time.Millisecond, 0)
	eventually(t, "first chunk buffered", func() bool { return buffered(pb) > 0 })
	for range 3 {
		out.Tick()
	}
	if n := audible(out.Rendered()); n != 0 || isClosed(pb.Started()) {
		t.Fatalf("40 ms buffered of a 120 ms pre-roll: %d audible samples, started=%v", n, isClosed(pb.Started()))
	}

	src.ch <- tone(100*time.Millisecond, 960)
	preroll := pb.(*playback).preroll
	eventually(t, "pre-roll buffered", func() bool { return buffered(pb) >= preroll })
	out.Tick()
	if !isClosed(pb.Started()) {
		t.Fatal("Started not closed once the pre-roll was buffered")
	}
	if got := out.Rendered()[3*period:]; audible(got) < len(got)*9/10 {
		t.Errorf("first period after pre-roll: %d of %d samples audible", audible(got), len(got))
	}
}

func TestEndOfStreamStartsShortClipBeforePreRoll(t *testing.T) {
	noLeaks(t)
	sink := &FakeSink{}
	src := newChanSource()
	pb, out := play(t, sink, src)

	src.ch <- tone(30*time.Millisecond, 0)
	close(src.ch)
	eventually(t, "end of stream", func() bool {
		p := pb.(*playback)
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.eof
	})
	for out.Tick() && !isClosed(pb.Done()) {
		time.Sleep(time.Millisecond)
	}
	if err := pb.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
	if !isClosed(pb.Started()) {
		t.Error("Started not closed for a clip shorter than the pre-roll")
	}
	// 30 ms at 48 kHz stereo, plus at most the resampler's padded tail.
	want := 2 * 1440
	if got := audible(out.Rendered()); got < want-8 || got > want+2*100 {
		t.Errorf("%d audible samples, want about %d", got, want)
	}
	if !src.closed.Load() || sink.Active() != 0 {
		t.Errorf("source closed %v, active outputs %d", src.closed.Load(), sink.Active())
	}
	if s := pb.Stats(); s.Underruns != 0 || s.Played < 29*time.Millisecond {
		t.Errorf("stats %+v", s)
	}
}

func TestUnderrunPlaysSilenceAndCountsOnce(t *testing.T) {
	noLeaks(t)
	sink := &FakeSink{}
	src := newChanSource()
	pb, out := play(t, sink, src)
	preroll := pb.(*playback).preroll

	src.ch <- tone(150*time.Millisecond, 0)
	eventually(t, "pre-roll", func() bool { return buffered(pb) >= preroll })
	for range 30 { // 300 ms of device time: the buffer runs dry halfway
		out.Tick()
	}
	tail := out.Rendered()[20*out.PeriodSamples():]
	if n := audible(tail); n != 0 {
		t.Errorf("%d audible samples during the underrun, want silence", n)
	}
	if u := pb.Stats().Underruns; u != 1 {
		t.Errorf("Underruns = %d after one long gap, want 1", u)
	}
	if isClosed(pb.Done()) {
		t.Fatal("playback ended on an underrun")
	}

	before := len(out.Rendered())
	src.ch <- tone(150*time.Millisecond, 3600)
	close(src.ch)
	eventually(t, "refill", func() bool { return buffered(pb) >= preroll })
	for out.Tick() && !isClosed(pb.Done()) {
		time.Sleep(time.Millisecond)
	}
	if err := pb.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
	if n := audible(out.Rendered()[before:]); n < 2*7000 {
		t.Errorf("only %d audible samples after the refill", n)
	}
	if u := pb.Stats().Underruns; u != 1 {
		t.Errorf("Underruns = %d, want 1 (end of stream is not an underrun)", u)
	}
}

func TestResamplesToDeviceFormat(t *testing.T) {
	noLeaks(t)
	sink := &FakeSink{Format: Format{SampleRate: 48000, Channels: 2}}
	src := &SliceSource{Chunks: [][]byte{tone(500*time.Millisecond, 0)}}
	pb, out := play(t, sink, src)
	for out.Tick() && !isClosed(pb.Done()) {
		time.Sleep(100 * time.Microsecond)
	}
	if err := pb.Wait(); err != nil {
		t.Fatal(err)
	}
	r := out.Rendered()
	var left []float64
	for i := 0; i+1 < len(r); i += 2 {
		if r[i] != r[i+1] {
			t.Fatalf("frame %d: channels differ (%v, %v)", i/2, r[i], r[i+1])
		}
		left = append(left, float64(r[i]))
	}
	// Find the audible span and check it is still a 1 kHz tone at 48 kHz.
	first, last := -1, -1
	for i, v := range left {
		if v != 0 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if span := last - first + 1; span < 24000-10 || span > 24000+100 {
		t.Fatalf("audible span %d frames, want about 24000", span)
	}
	mid := left[first+2400 : last-2400]
	amp := goertzel(mid, 48000, 1000)
	if db := 20 * math.Log10(amp/(8000.0/32768)); math.Abs(db) > 0.1 {
		t.Errorf("1 kHz tone after resampling: gain %.3f dB", db)
	}
	if off := goertzel(mid, 48000, 2000); off > amp*1e-3 {
		t.Errorf("energy at 2 kHz: %.5f vs %.5f at 1 kHz; wrong rate?", off, amp)
	}
}

func goertzel(x []float64, rate int, freq float64) float64 {
	w := 2 * math.Pi * freq / float64(rate)
	var re, im, wsum float64
	for i, v := range x {
		h := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(len(x)-1))
		re += v * h * math.Cos(w*float64(i))
		im -= v * h * math.Sin(w*float64(i))
		wsum += h
	}
	return 2 * math.Hypot(re, im) / wsum
}

// TestStopLatency runs the device clock at real-time pace and checks the
// NFR-001 budget both on that clock and on the wall clock.
func TestStopLatency(t *testing.T) {
	noLeaks(t)
	tests := []struct {
		name string
		stop func(pb Playback, cancel context.CancelFunc)
		want error
	}{
		{"Stop", func(pb Playback, _ context.CancelFunc) { pb.Stop() }, ErrStopped},
		{"ctx", func(pb Playback, cancel context.CancelFunc) { cancel(); <-pb.Done() }, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &FakeSink{Realtime: true, Period: 10 * time.Millisecond}
			chunks := make([][]byte, 50)
			for i := range chunks {
				chunks[i] = tone(100*time.Millisecond, i*2400)
			}
			src := &SliceSource{Chunks: chunks, Hold: true}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pb, err := New(sink).Play(ctx, src, mono24k)
			if err != nil {
				t.Fatal(err)
			}
			out := sink.Last()
			select {
			case <-pb.Started():
			case <-time.After(time.Second):
				t.Fatal("never started")
			}
			time.Sleep(50 * time.Millisecond)

			at := out.Ticks()
			t0 := time.Now()
			tt.stop(pb, cancel)
			wall := time.Since(t0)

			closed, closedAt := out.Closed()
			if !closed {
				t.Fatal("device not closed")
			}
			// Device time from the stop request until the device stopped.
			devTime := time.Duration(closedAt-at) * out.Period()
			t.Logf("stop: %v wall, %v device time", wall, devTime)
			if wall > 150*time.Millisecond || devTime > 150*time.Millisecond {
				t.Errorf("stop took %v wall, %v device time; budget 150 ms", wall, devTime)
			}
			// At most the period in flight at the request may still carry
			// audio; everything rendered after it is silence.
			if r := out.Rendered(); len(r) > (at+1)*out.PeriodSamples() {
				if n := audible(r[(at+1)*out.PeriodSamples():]); n != 0 {
					t.Errorf("%d audible samples rendered after stop", n)
				}
			}
			if err := pb.Wait(); !errors.Is(err, tt.want) {
				t.Errorf("Wait = %v, want %v", err, tt.want)
			}
			if !src.IsClosed() || sink.Active() != 0 {
				t.Errorf("source closed %v, active outputs %d", src.IsClosed(), sink.Active())
			}
		})
	}
}

func TestDeviceLoss(t *testing.T) {
	noLeaks(t)
	tests := []struct {
		name string
		kill func(*FakeOutput)
	}{
		{"reported", (*FakeOutput).Lose},
		{"silent stall", (*FakeOutput).Hang},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &FakeSink{Realtime: true}
			src := &SliceSource{Chunks: [][]byte{tone(time.Second, 0)}, Hold: true}
			pb, out := play(t, sink, src, WithStallTimeout(100*time.Millisecond))
			<-pb.Started()
			tt.kill(out)
			select {
			case <-pb.Done():
			case <-time.After(time.Second):
				t.Fatal("playback hung after device loss")
			}
			if err := pb.Wait(); !errors.Is(err, ErrDeviceLost) {
				t.Errorf("Wait = %v, want ErrDeviceLost", err)
			}
			if !src.IsClosed() || sink.Active() != 0 {
				t.Errorf("source closed %v, active outputs %d", src.IsClosed(), sink.Active())
			}
		})
	}
}

func TestSourceErrorEndsAfterBufferedAudio(t *testing.T) {
	noLeaks(t)
	boom := errors.New("stream cut")
	sink := &FakeSink{Realtime: true}
	src := &SliceSource{Chunks: [][]byte{tone(50*time.Millisecond, 0)}, Err: boom}
	pb, out := play(t, sink, src)
	if err := pb.Wait(); !errors.Is(err, boom) {
		t.Fatalf("Wait = %v, want %v", err, boom)
	}
	if n := audible(out.Rendered()); n < 2*2400-8 {
		t.Errorf("only %d audible samples of the audio received before the error", n)
	}
}

func TestPlayErrors(t *testing.T) {
	noLeaks(t)
	openErr := errors.New("no device")
	tests := []struct {
		name string
		sink *FakeSink
		ctx  func() context.Context
		f    Format
	}{
		{"bad format", &FakeSink{}, context.Background, Format{}},
		{"open fails", &FakeSink{OpenErr: openErr}, context.Background, mono24k},
		{"cancelled", &FakeSink{}, func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, mono24k},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &SliceSource{}
			if _, err := New(tt.sink).Play(tt.ctx(), src, tt.f); err == nil {
				t.Fatal("Play succeeded")
			}
			if !src.IsClosed() || tt.sink.Active() != 0 {
				t.Errorf("source closed %v, active outputs %d", src.IsClosed(), tt.sink.Active())
			}
		})
	}
}

func TestFromTTS(t *testing.T) {
	noLeaks(t)
	p := &tts.Fake{Chunks: 3, ChunkBytes: 4800}
	ctx := context.Background()
	s, err := p.SynthesizeStream(ctx, tts.SynthesisRequest{Text: "x", VoiceID: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	src, f, err := FromTTS(s, tts.PCM24kMono)
	if err != nil {
		t.Fatal(err)
	}
	if f != mono24k {
		t.Fatalf("format %+v", f)
	}
	sink := &FakeSink{Realtime: true}
	pb, err := New(sink).Play(ctx, src, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := pb.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := pb.Stats().Played; got < 299*time.Millisecond {
		t.Errorf("played %v of 300 ms", got)
	}

	s, _ = p.SynthesizeStream(ctx, tts.SynthesisRequest{Text: "x", VoiceID: "fake"})
	if _, _, err := FromTTS(s, tts.MP3); err == nil {
		t.Error("FromTTS accepted MP3")
	}
}

func TestFakePlayer(t *testing.T) {
	noLeaks(t)
	fp := &FakePlayer{Realtime: true}
	src := &SliceSource{Chunks: [][]byte{tone(time.Second, 0)}}
	pb, err := fp.Play(context.Background(), src, mono24k)
	if err != nil {
		t.Fatal(err)
	}
	<-pb.Started()
	t0 := time.Now()
	pb.Stop()
	if d := time.Since(t0); d > 50*time.Millisecond {
		t.Errorf("Stop took %v", d)
	}
	if err := pb.Wait(); !errors.Is(err, ErrStopped) {
		t.Errorf("Wait = %v", err)
	}
	if plays := fp.Plays(); len(plays) != 1 || fp.Active() != 0 || !src.IsClosed() {
		t.Errorf("plays %+v, active %d, closed %v", plays, fp.Active(), src.IsClosed())
	}
}
