// Package audio turns raw microphone input into the PCM16 mono 16 kHz frames
// the STT sidecar expects (PRD §9.5, OC-34).
//
// Platform backends (internal/platform/audio) only deliver device-native
// float32 audio through Source; downmixing, resampling, framing and level
// metering happen here in pure Go so every OS behaves the same.
//
// Raw audio is user content: it must never be written to disk or logs
// (FR-VOICE-012). Nothing in this package does either.
package audio

import (
	"context"
	"errors"
	"time"
)

// Output format of every Stream.
const (
	SampleRate    = 16000
	FrameDuration = 20 * time.Millisecond
	FrameSamples  = SampleRate * int(FrameDuration/time.Millisecond) / 1000 // 320
)

var (
	// ErrDeviceLost means the device disappeared or stopped delivering audio
	// mid-stream (unplugged, driver reset, server restart).
	ErrDeviceLost = errors.New("audio: capture device lost")
	// ErrDeviceNotFound means the requested device ID is not present.
	ErrDeviceNotFound = errors.New("audio: capture device not found")
)

// Device is a capture endpoint. ID is opaque and stable across sessions as far
// as the OS allows, so it can be stored in settings.
type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// Frame is FrameDuration of PCM16 mono audio at SampleRate. At is the offset
// of the first sample from the start of the stream, derived from the sample
// count rather than the wall clock, so it never drifts or jumps.
type Frame struct {
	Samples []int16
	At      time.Duration
}

// Stream is one capture session.
type Stream interface {
	// Frames delivers audio until the stream ends; the channel is then
	// closed. Each Frame owns its Samples slice.
	Frames() <-chan Frame
	// Err reports why Frames was closed: nil after Close or a clean end of
	// input, the context error after cancellation, ErrDeviceLost if the
	// device went away. Only meaningful once Frames is closed.
	Err() error
	// Level is the level of the most recently delivered frame.
	Level() Level
	// Close stops capture and releases the device. It blocks until the
	// backend is released and is safe to call more than once.
	Close() error
}

// Capturer enumerates and opens microphones. Open resolves the device at call
// time, so a device chosen in settings applies to the next session without
// restarting the app (FR-STT-009).
type Capturer interface {
	Devices(ctx context.Context) ([]Device, error)
	// Open starts capture from deviceID ("" means the system default). ctx
	// bounds the whole stream: cancelling it stops capture.
	Open(ctx context.Context, deviceID string) (Stream, error)
}

// Format is the native format a RawStream delivers.
type Format struct {
	SampleRate int
	Channels   int
}

// RawStream is device-native audio from a platform backend.
type RawStream interface {
	Format() Format
	// Chunks delivers interleaved float32 samples in [-1, 1]. The backend
	// never blocks its audio thread on a slow reader; it drops instead and
	// counts the loss in Dropped. The channel is closed when the device
	// stops, after Close or on loss.
	Chunks() <-chan []float32
	// Err is nil for a clean end or Close, ErrDeviceLost otherwise. Only
	// meaningful once Chunks is closed.
	Err() error
	// Dropped is the number of chunks discarded because the reader lagged.
	Dropped() uint64
	Close() error
}

// Source is what a platform backend implements.
type Source interface {
	Devices(ctx context.Context) ([]Device, error)
	// OpenRaw starts capture at the device's native rate and channel count.
	// deviceID "" selects the system default.
	OpenRaw(ctx context.Context, deviceID string) (RawStream, error)
}
