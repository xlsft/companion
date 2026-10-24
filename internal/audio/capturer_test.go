package audio

import (
	"context"
	"errors"
	"math"
	"runtime"
	"testing"
	"time"
)

// drain reads every frame until the stream ends or the deadline passes.
func drain(t *testing.T, s Stream, deadline time.Duration) []Frame {
	t.Helper()
	var frames []Frame
	timeout := time.After(deadline)
	for {
		select {
		case f, ok := <-s.Frames():
			if !ok {
				return frames
			}
			frames = append(frames, f)
		case <-timeout:
			t.Fatalf("stream did not end within %v", deadline)
		}
	}
}

// checkNoLeak fails if goroutines started during the test are still running.
func checkNoLeak(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if n := runtime.NumGoroutine(); n > before {
			t.Errorf("goroutine leak: %d before, %d after", before, n)
		}
	})
}

func TestStreamFramesAndLevel(t *testing.T) {
	tests := []struct {
		name      string
		src       *FakeSource
		rms, peak float64 // expected dBFS
	}{
		{"sine 48k stereo", &FakeSource{Freq: 1000, Amplitude: 0.5}, -9.03, -6.02},
		{"sine 44.1k mono", &FakeSource{Freq: 440, Amplitude: 0.25, Format: Format{44100, 1}}, -15.05, -12.04},
		{"sine 96k 6ch", &FakeSource{Freq: 2000, Amplitude: 0.5, Format: Format{96000, 6}}, -9.03, -6.02},
		{"silence", &FakeSource{Signal: SignalSilence}, MinDBFS, MinDBFS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkNoLeak(t)
			src := tt.src
			src.EndAfter = time.Second
			s, err := NewCapturer(src).Open(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			frames := drain(t, s, 5*time.Second)
			if err := s.Err(); err != nil {
				t.Fatalf("Err = %v, want nil after clean end", err)
			}
			// One second in, 50 frames out minus the part held by the filter.
			if len(frames) < 49 || len(frames) > 50 {
				t.Fatalf("got %d frames, want 49 or 50", len(frames))
			}
			for i, f := range frames {
				if len(f.Samples) != FrameSamples {
					t.Fatalf("frame %d has %d samples", i, len(f.Samples))
				}
				if want := time.Duration(i) * FrameDuration; f.At != want {
					t.Fatalf("frame %d At = %v, want %v", i, f.At, want)
				}
			}
			lvl := s.Level()
			if math.Abs(lvl.RMS-tt.rms) > 0.1 || math.Abs(lvl.Peak-tt.peak) > 0.1 {
				t.Fatalf("Level = %+v, want rms %.2f peak %.2f", lvl, tt.rms, tt.peak)
			}
			if src.Active() != 0 {
				t.Fatal("raw stream not released")
			}
		})
	}
}

func TestStreamEndings(t *testing.T) {
	tests := []struct {
		name    string
		src     *FakeSource
		opts    []Option
		wantErr error
		within  time.Duration
	}{
		{"device lost", &FakeSource{LoseAfter: 300 * time.Millisecond}, nil, ErrDeviceLost, time.Second},
		{"backend hangs", &FakeSource{HangAfter: 100 * time.Millisecond}, []Option{WithStallTimeout(100 * time.Millisecond)}, ErrDeviceLost, time.Second},
		{"clean end", &FakeSource{EndAfter: 100 * time.Millisecond}, nil, nil, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkNoLeak(t)
			src := tt.src
			s, err := NewCapturer(src, tt.opts...).Open(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			drain(t, s, tt.within)
			if err := s.Err(); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Err = %v, want %v", err, tt.wantErr)
			}
			if src.Active() != 0 {
				t.Fatal("raw stream not released")
			}
		})
	}
}

func TestStreamCancelStopsQuickly(t *testing.T) {
	checkNoLeak(t)
	src := &FakeSource{Realtime: true}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := NewCapturer(src).Open(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	<-s.Frames() // capture is running
	start := time.Now()
	cancel()
	drain(t, s, time.Second)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("stopped %v after cancel, want <= 100ms", d)
	}
	if !errors.Is(s.Err(), context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", s.Err())
	}
	if src.Active() != 0 {
		t.Fatal("raw stream not released after cancel")
	}
}

func TestStreamCloseWithUnreadFrames(t *testing.T) {
	checkNoLeak(t)
	src := &FakeSource{} // fast and endless: the frame buffer fills up
	s, err := NewCapturer(src).Open(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		s.Close()
		s.Close() // idempotent
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close blocked with a full frame buffer")
	}
	if s.Err() != nil {
		t.Fatalf("Err = %v after Close, want nil", s.Err())
	}
	if src.Active() != 0 {
		t.Fatal("raw stream not released after Close")
	}
}

func TestSlowReaderIsNotAStall(t *testing.T) {
	checkNoLeak(t)
	src := &FakeSource{}
	s, err := NewCapturer(src, WithStallTimeout(30*time.Millisecond)).Open(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for range 3 {
		time.Sleep(60 * time.Millisecond) // reader busy longer than the stall timeout
		for range frameBuffer + 5 {
			if _, ok := <-s.Frames(); !ok {
				t.Fatalf("stream ended under a slow reader: %v", s.Err())
			}
		}
	}
}

func TestOpenSelectsDevicePerSession(t *testing.T) {
	checkNoLeak(t)
	src := &FakeSource{EndAfter: 40 * time.Millisecond, DeviceList: []Device{
		{ID: "a", Name: "Built-in", Default: true},
		{ID: "b", Name: "Headset"},
	}}
	c := NewCapturer(src)
	devs, err := c.Devices(context.Background())
	if err != nil || len(devs) != 2 {
		t.Fatalf("Devices = %v, %v", devs, err)
	}
	// Settings change between sessions: each Open picks up the new choice.
	for _, id := range []string{"", "a", "b", "a"} {
		s, err := c.Open(context.Background(), id)
		if err != nil {
			t.Fatalf("Open(%q): %v", id, err)
		}
		drain(t, s, time.Second)
		s.Close()
	}
	if _, err := c.Open(context.Background(), "gone"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("Open(unknown) err = %v, want ErrDeviceNotFound", err)
	}
	if src.Opened() != 4 || src.Active() != 0 {
		t.Fatalf("opened %d active %d, want 4 and 0", src.Opened(), src.Active())
	}
}

func TestStreamResamplesToTone(t *testing.T) {
	// End-to-end: a 1 kHz tone at 44.1 kHz stereo comes out as 1 kHz PCM16.
	src := &FakeSource{Freq: 1000, Amplitude: 0.5, Format: Format{44100, 2}, EndAfter: time.Second}
	s, err := NewCapturer(src).Open(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var pcm []float64
	for _, f := range drain(t, s, 5*time.Second) {
		for _, v := range f.Samples {
			pcm = append(pcm, float64(v)/32767)
		}
	}
	x := steady(pcm)
	if got := goertzelAmp(x, SampleRate, 1000); math.Abs(got-0.5) > 0.005 || got/(rms(x)*math.Sqrt2) < 0.999 {
		t.Fatalf("1 kHz amplitude %.4f of total %.4f", got, rms(x)*math.Sqrt2)
	}
}
