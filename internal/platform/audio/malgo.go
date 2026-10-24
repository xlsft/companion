//go:build cgo

package audio

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"
	core "github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/pkg/contracts"
)

// periodMS keeps callbacks small so cancellation and device release are quick
// (Close must finish well inside the 100 ms cancel budget).
const periodMS = 10

// chunkBuffer is how many periods (~640 ms) may queue for a lagging pipeline
// before the callback starts dropping.
const chunkBuffer = 64

type source struct{}

// New returns the miniaudio-backed Source (WASAPI, CoreAudio, PulseAudio /
// PipeWire, ALSA, picked by miniaudio in that order of preference per OS).
func New() core.Source { return source{} }

// micErr ties backend failures to the microphone capability. miniaudio
// errors are result-code names only, so they are safe to log.
func micErr(op string, err error) error {
	return &contracts.CapabilityError{Capability: contracts.CapMicrophone, Err: errors.New("audio: " + op + ": " + err.Error())}
}

// A fresh miniaudio context per call keeps enumeration and the default device
// current after hotplug, at the cost of one server connection per session.
func newContext() (*malgo.AllocatedContext, error) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, micErr("init context", err)
	}
	return mctx, nil
}

func freeContext(mctx *malgo.AllocatedContext) {
	_ = mctx.Uninit()
	mctx.Free()
}

// async runs fn off the caller's goroutine so a hung audio server cannot block
// past ctx; if ctx wins, cleanup receives fn's result once it arrives.
func async[T any](ctx context.Context, fn func() (T, error), cleanup func(T)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.err == nil && cleanup != nil {
				cleanup(r.v)
			}
		}()
		var zero T
		return zero, ctx.Err()
	}
}

func (source) Devices(ctx context.Context) ([]core.Device, error) {
	return async(ctx, func() ([]core.Device, error) {
		mctx, err := newContext()
		if err != nil {
			return nil, err
		}
		defer freeContext(mctx)
		infos, err := mctx.Devices(malgo.Capture)
		if err != nil {
			return nil, micErr("enumerate", err)
		}
		devs := make([]core.Device, 0, len(infos))
		for _, in := range infos {
			devs = append(devs, core.Device{ID: in.ID.String(), Name: in.Name(), Default: in.IsDefault != 0})
		}
		return devs, nil
	}, nil)
}

func (source) OpenRaw(ctx context.Context, deviceID string) (core.RawStream, error) {
	return async(ctx, func() (core.RawStream, error) { return open(deviceID) },
		func(s core.RawStream) { s.Close() })
}

func open(deviceID string) (core.RawStream, error) {
	mctx, err := newContext()
	if err != nil {
		return nil, err
	}
	var idPtr unsafe.Pointer
	if deviceID != "" {
		id, ok := findDevice(mctx, malgo.Capture, deviceID)
		if !ok {
			freeContext(mctx)
			return nil, core.ErrDeviceNotFound
		}
		// miniaudio copies the id during init, so C memory can go right after.
		idPtr = id.Pointer()
		defer C.free(idPtr)
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.DeviceID = idPtr
	cfg.Capture.Format = malgo.FormatF32
	cfg.Capture.Channels = 0 // native
	cfg.SampleRate = 0       // native; resampling happens in Go
	cfg.PeriodSizeInMilliseconds = periodMS

	r := &rawStream{mctx: mctx, chunks: make(chan []float32, chunkBuffer)}
	dev, err := malgo.InitDevice(mctx.Context, cfg, malgo.DeviceCallbacks{
		Data: r.onData,
		// Fires only when the device stops on its own: malgo unregisters it
		// before our own Uninit.
		Stop: func() { r.finish(core.ErrDeviceLost) },
	})
	if err != nil {
		freeContext(mctx)
		return nil, micErr("init device", err)
	}
	r.dev = dev
	r.format = core.Format{SampleRate: int(dev.SampleRate()), Channels: int(dev.CaptureChannels())}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		freeContext(mctx)
		return nil, micErr("start device", err)
	}
	return r, nil
}

func findDevice(mctx *malgo.AllocatedContext, kind malgo.DeviceType, id string) (malgo.DeviceID, bool) {
	want, err := hex.DecodeString(id)
	if err != nil {
		return malgo.DeviceID{}, false
	}
	infos, err := mctx.Devices(kind)
	if err != nil {
		return malgo.DeviceID{}, false
	}
	for _, in := range infos {
		if in.ID.String() == id {
			var out malgo.DeviceID
			copy(out[:], want)
			return out, true
		}
	}
	return malgo.DeviceID{}, false
}

type rawStream struct {
	mctx    *malgo.AllocatedContext
	dev     *malgo.Device
	format  core.Format
	chunks  chan []float32
	dropped atomic.Uint64

	mu     sync.Mutex
	ended  bool
	err    error
	closer sync.Once
}

func (r *rawStream) Format() core.Format      { return r.format }
func (r *rawStream) Chunks() <-chan []float32 { return r.chunks }
func (r *rawStream) Dropped() uint64          { return r.dropped.Load() }

func (r *rawStream) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// onData runs on miniaudio's realtime thread: copy and hand off, never block.
func (r *rawStream) onData(_, in []byte, _ uint32) {
	n := len(in) / 4
	if n == 0 {
		return
	}
	buf := make([]float32, n)
	copy(buf, unsafe.Slice((*float32)(unsafe.Pointer(unsafe.SliceData(in))), n))
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	select {
	case r.chunks <- buf:
	default:
		r.dropped.Add(1)
	}
}

func (r *rawStream) finish(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	r.ended, r.err = true, err
	close(r.chunks)
}

func (r *rawStream) Close() error {
	r.closer.Do(func() {
		// Stop before Uninit: ma_device_uninit only signals the worker's
		// wakeup event, but the PulseAudio worker sits in a blocking
		// pa_mainloop_iterate. On a live stream data wakes it every period;
		// on a killed one (source unplugged) nothing ever does and Uninit
		// hangs forever. Stop wakes the mainloop explicitly.
		_ = r.dev.Stop()
		r.dev.Uninit() // waits for the audio thread; no callbacks after this
		freeContext(r.mctx)
		r.finish(nil)
	})
	return nil
}
