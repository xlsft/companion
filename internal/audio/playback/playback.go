// Package playback plays streamed PCM16 speech through an output device as
// it arrives (PRD §9.6, OC-64): a jitter buffer with pre-roll absorbs network
// jitter, provider audio is resampled to the device rate, and Stop is a hard
// cancel that silences the device within one period (NFR-001: ≤150 ms).
//
// Platform backends (internal/platform/audio) only open a device and call a
// render function from their audio thread; buffering, conversion and the
// playback state machine live here in pure Go. Speed is not handled here:
// providers synthesise at the requested speed (FR-VOICE-010).
//
// Audio is user content: nothing in this package logs or stores it.
package playback

import (
	"context"
	"errors"
	"time"

	"github.com/xlsft/companion/internal/audio"
)

// Format is interleaved PCM: the source format given to Play, or a device's
// native format.
type Format = audio.Format

var (
	// ErrStopped is what Wait returns after Stop.
	ErrStopped = errors.New("playback: stopped")
	// ErrDeviceLost means the output device disappeared or stopped pulling
	// audio mid-playback (unplugged, driver reset, server restart).
	ErrDeviceLost = errors.New("playback: output device lost")
	// ErrDeviceNotFound means the requested device ID is not present.
	ErrDeviceNotFound = errors.New("playback: output device not found")
)

// ChunkSource yields interleaved little-endian PCM16. Next returns io.EOF
// after the last chunk and must honour ctx, which is how a hard cancel aborts
// a slow network read. FromTTS adapts a tts.Stream.
type ChunkSource interface {
	Next(ctx context.Context) ([]byte, error)
	// Close aborts the source. Idempotent.
	Close() error
}

// Player starts playbacks.
type Player interface {
	// Play takes ownership of src (it is closed when playback ends) and
	// plays it, given in format f, on the output device. It returns once the
	// device is open; audio follows as src delivers. ctx bounds the whole
	// playback: cancelling it is the same hard cancel as Stop.
	Play(ctx context.Context, src ChunkSource, f Format) (Playback, error)
}

// Playback is one running playback.
type Playback interface {
	// Started is closed when the first sample is handed to the device. It
	// stays open if the playback ends before any audio was played.
	Started() <-chan struct{}
	// Done is closed when the playback has ended and the device is released.
	Done() <-chan struct{}
	// Wait blocks until Done and returns why the playback ended: nil after
	// the whole source was played, ErrStopped after Stop, the context error
	// after cancellation, ErrDeviceLost, or the source's error once the
	// audio received before it was played.
	Wait() error
	// Stop silences the device at once, discarding buffered audio, aborts
	// the source and releases the device. It blocks until that is done and
	// is safe to call more than once or after the end.
	Stop()
	// Stats is safe to call at any time.
	Stats() Stats
}

// Stats describes the health of a playback, for diagnostics.
type Stats struct {
	// Underruns counts how often the buffer ran dry before the source
	// ended; each episode is counted once however long it lasts.
	Underruns int
	// Played is the duration of source audio handed to the device.
	Played time.Duration
}

// Output is an open device as a platform backend provides it.
type Output interface {
	// Format is the device's native format; Play converts to it.
	Format() Format
	// Start begins calling render from the audio thread. render fills its
	// whole argument with interleaved float32 samples in Format and never
	// blocks for long.
	Start(render func(out []float32)) error
	// Latency is how long a rendered sample may sit in device buffers
	// before it is heard; the playback ends that long after its last sample.
	Latency() time.Duration
	// Lost is closed when the device stops on its own.
	Lost() <-chan struct{}
	// Close stops the device immediately, without draining what it has
	// buffered, and releases it. No render call happens after it returns.
	// Idempotent.
	Close() error
}

// Sink opens output devices; platform backends implement it.
type Sink interface {
	// OpenOutput opens deviceID ("" means the system default) at its native
	// rate and channel count.
	OpenOutput(ctx context.Context, deviceID string) (Output, error)
}
