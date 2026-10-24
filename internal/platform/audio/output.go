//go:build cgo

package audio

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"
	"github.com/xlsft/openclick/internal/audio/playback"
	"github.com/xlsft/openclick/pkg/contracts"
)

// A 10 ms period bounds how long a Stop waits for the sample in flight;
// three of them are enough headroom for a desktop scheduler without the
// buffer adding audible latency to the hard cancel (NFR-001).
const (
	outPeriodMS = 10
	outPeriods  = 3
)

type sink struct{}

// NewSink returns the miniaudio-backed playback Sink.
func NewSink() playback.Sink { return sink{} }

func outErr(op string, err error) error {
	return &contracts.CapabilityError{Capability: contracts.CapAudioOutput, Err: errors.New("audio: " + op + ": " + err.Error())}
}

func (sink) OpenOutput(ctx context.Context, deviceID string) (playback.Output, error) {
	return async(ctx, func() (playback.Output, error) { return openOutput(deviceID) },
		func(o playback.Output) { o.Close() })
}

func openOutput(deviceID string) (playback.Output, error) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, outErr("init context", err)
	}
	var idPtr unsafe.Pointer
	if deviceID != "" {
		id, ok := findDevice(mctx, malgo.Playback, deviceID)
		if !ok {
			freeContext(mctx)
			return nil, playback.ErrDeviceNotFound
		}
		idPtr = id.Pointer()
		defer C.free(idPtr)
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.DeviceID = idPtr
	cfg.Playback.Format = malgo.FormatF32
	cfg.Playback.Channels = 0 // native
	cfg.SampleRate = 0        // native; resampling happens in Go
	cfg.PeriodSizeInMilliseconds = outPeriodMS
	cfg.Periods = outPeriods

	o := &output{mctx: mctx, lost: make(chan struct{})}
	dev, err := malgo.InitDevice(mctx.Context, cfg, malgo.DeviceCallbacks{
		Data: o.onData,
		Stop: o.onStop,
	})
	if err != nil {
		freeContext(mctx)
		return nil, outErr("init device", err)
	}
	o.dev = dev
	o.format = playback.Format{SampleRate: int(dev.SampleRate()), Channels: int(dev.PlaybackChannels())}
	return o, nil
}

type output struct {
	mctx   *malgo.AllocatedContext
	dev    *malgo.Device
	format playback.Format
	render func([]float32) // set before the device starts, never changed

	lost     chan struct{}
	lostOnce sync.Once
	closing  atomic.Bool
	closer   sync.Once
}

func (o *output) Format() playback.Format { return o.format }
func (o *output) Lost() <-chan struct{}   { return o.lost }

func (o *output) Latency() time.Duration {
	return outPeriods * outPeriodMS * time.Millisecond
}

func (o *output) Start(render func([]float32)) error {
	o.render = render
	if err := o.dev.Start(); err != nil {
		return outErr("start device", err)
	}
	return nil
}

// onData runs on miniaudio's realtime thread.
func (o *output) onData(out, _ []byte, _ uint32) {
	n := len(out) / 4
	if n == 0 {
		return
	}
	o.render(unsafe.Slice((*float32)(unsafe.Pointer(unsafe.SliceData(out))), n))
}

func (o *output) onStop() {
	// Our own Close stops the device too; only an unrequested stop is a
	// loss.
	if !o.closing.Load() {
		o.lostOnce.Do(func() { close(o.lost) })
	}
}

func (o *output) Close() error {
	o.closer.Do(func() {
		o.closing.Store(true)
		// Stop corks the stream without draining (miniaudio skips the
		// PulseAudio drain), so buffered audio is dropped, and it wakes a
		// PulseAudio mainloop that Uninit alone could leave blocked forever
		// on a vanished sink (see rawStream.Close).
		_ = o.dev.Stop()
		o.dev.Uninit()
		freeContext(o.mctx)
	})
	return nil
}
