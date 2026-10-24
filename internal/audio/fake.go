package audio

import (
	"context"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// Signal selects what a FakeSource generates.
type Signal int

const (
	SignalSine Signal = iota
	SignalSilence
	SignalNoise // deterministic white noise
)

// FakeDevice is the only device a FakeSource reports unless Devices is set.
var FakeDevice = Device{ID: "fake:default", Name: "Fake microphone", Default: true}

// FakeSource is a Source that synthesises audio, for tests in packages that
// consume a Capturer. Use it as NewCapturer(&FakeSource{...}) so the real
// resampling and framing path runs. Zero value: 440 Hz sine at -6 dBFS,
// 48 kHz stereo, delivered as fast as the reader consumes it, forever.
type FakeSource struct {
	Signal    Signal
	Freq      float64 // Hz, default 440
	Amplitude float64 // linear, default 0.5
	Format    Format  // default 48000 Hz, 2 channels
	// ChunkFrames is native frames per chunk, default 10 ms worth.
	ChunkFrames int
	// Realtime paces chunks at the wall clock instead of as fast as possible.
	Realtime bool
	// EndAfter > 0 ends the input cleanly after that much audio.
	EndAfter time.Duration
	// LoseAfter > 0 simulates an unplug: Chunks closes with ErrDeviceLost.
	LoseAfter time.Duration
	// HangAfter > 0 stops delivering without closing, like a backend that
	// silently stops calling back.
	HangAfter time.Duration
	// BlockClose, if set, makes raw Close wait until it is closed, like a
	// backend that wedges while releasing the device.
	BlockClose chan struct{}
	// Devices overrides the device list; default is [FakeDevice].
	DeviceList []Device

	active atomic.Int32
	opened atomic.Int32
}

// Active is the number of raw streams opened and not yet released.
func (f *FakeSource) Active() int { return int(f.active.Load()) }

// Opened is the total number of raw streams opened.
func (f *FakeSource) Opened() int { return int(f.opened.Load()) }

func (f *FakeSource) devices() []Device {
	if f.DeviceList != nil {
		return f.DeviceList
	}
	return []Device{FakeDevice}
}

func (f *FakeSource) Devices(ctx context.Context) ([]Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]Device(nil), f.devices()...), nil
}

func (f *FakeSource) OpenRaw(ctx context.Context, deviceID string) (RawStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found := false
	for _, d := range f.devices() {
		if d.ID == deviceID || (deviceID == "" && d.Default) {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrDeviceNotFound
	}
	format := f.Format
	if format.SampleRate == 0 {
		format.SampleRate = 48000
	}
	if format.Channels == 0 {
		format.Channels = 2
	}
	r := &fakeRaw{
		src:    f,
		format: format,
		chunks: make(chan []float32, 8),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	f.active.Add(1)
	f.opened.Add(1)
	go r.run()
	return r, nil
}

type fakeRaw struct {
	src      *FakeSource
	format   Format
	chunks   chan []float32
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	err      atomic.Pointer[error]
}

func (r *fakeRaw) Format() Format           { return r.format }
func (r *fakeRaw) Chunks() <-chan []float32 { return r.chunks }
func (r *fakeRaw) Dropped() uint64          { return 0 }

func (r *fakeRaw) Err() error {
	if p := r.err.Load(); p != nil {
		return *p
	}
	return nil
}

func (r *fakeRaw) Close() error {
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done
	if r.src.BlockClose != nil {
		<-r.src.BlockClose
	}
	return nil
}

func (r *fakeRaw) run() {
	defer close(r.done)
	defer r.src.active.Add(-1)
	defer close(r.chunks)

	f := r.src
	rate, ch := r.format.SampleRate, r.format.Channels
	chunkFrames := f.ChunkFrames
	if chunkFrames <= 0 {
		chunkFrames = rate / 100
	}
	freq, amp := f.Freq, f.Amplitude
	if freq == 0 {
		freq = 440
	}
	if amp == 0 {
		amp = 0.5
	}
	limit := func(d time.Duration) int64 {
		if d <= 0 {
			return math.MaxInt64
		}
		return int64(d.Seconds() * float64(rate))
	}
	endAt, loseAt, hangAt := limit(f.EndAfter), limit(f.LoseAfter), limit(f.HangAfter)
	rng := rand.New(rand.NewPCG(1, 2))
	start := time.Now()

	for n := int64(0); ; n += int64(chunkFrames) {
		switch {
		case n >= loseAt:
			err := ErrDeviceLost
			r.err.Store(&err)
			return
		case n >= endAt:
			return
		case n >= hangAt:
			<-r.stop
			return
		}
		buf := make([]float32, chunkFrames*ch)
		for i := range chunkFrames {
			var v float64
			switch f.Signal {
			case SignalSine:
				v = amp * math.Sin(2*math.Pi*freq*float64(n+int64(i))/float64(rate))
			case SignalNoise:
				v = amp * (2*rng.Float64() - 1)
			}
			for c := range ch {
				buf[i*ch+c] = float32(v)
			}
		}
		if f.Realtime {
			due := start.Add(time.Duration(n+int64(chunkFrames)) * time.Second / time.Duration(rate))
			select {
			case <-time.After(time.Until(due)):
			case <-r.stop:
				return
			}
		}
		select {
		case r.chunks <- buf:
		case <-r.stop:
			return
		}
	}
}
