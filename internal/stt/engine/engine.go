// Package engine is the seam between the sidecar protocol loop and a speech
// recognition runtime. Engines register themselves by name; the sidecar
// binary picks one with -engine.
package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Engine transcribes complete buffers. Implementations must be safe for
// concurrent calls (serialising internally if the runtime is not) and must
// return promptly with ctx.Err() once ctx is cancelled.
type Engine interface {
	Transcribe(ctx context.Context, pcm []float32, opts Options) (Result, error)
	Close() error
}

type Options struct {
	// Language is "auto" or an ISO 639-1 code.
	Language string
	// Partial requests a cheaper decode for rolling partials (single
	// segment, no timestamps); quality may be lower.
	Partial bool
}

type Result struct {
	Text string
	// Language is the detected language for "auto", else the requested one.
	Language string
	Segments []Segment
}

type Segment struct {
	Start, End time.Duration
	Text       string
}

type Config struct {
	ModelPath string
	Language  string
	// Backend is "cpu" or "auto".
	Backend string
	Threads int
}

// Info describes the runtime an engine actually started with.
type Info struct {
	// Engine names the engine that loaded the model when a dispatcher
	// (the sidecar's "auto") chose it; empty means the registered name.
	Engine         string
	RuntimeVersion string
	BackendInUse   string
	// FallbackReason is non-empty when Backend "auto" fell back to CPU.
	FallbackReason string
	Languages      []string
}

// Sentinel errors a Factory returns so the sidecar can map them to protocol
// error codes; wrap them to add technical detail.
var (
	ErrModelNotFound = errors.New("model not found")
	ErrBackendInit   = errors.New("backend init failed")
)

type Factory func(Config) (Engine, Info, error)

// Streamer is implemented by engines whose model decodes incrementally
// (streaming transducers). The sidecar then feeds every chunk to a Stream
// and reports its hypothesis as the partial, instead of re-decoding a
// trailing window. NewStream returns ErrNotStreaming when the loaded model
// is offline, so one engine can serve both kinds.
type Streamer interface {
	NewStream(opts Options) (Stream, error)
}

// Stream is one utterance of a streaming model. It is used from a single
// goroutine.
type Stream interface {
	// Accept appends audio, decodes what is ready and returns the current
	// hypothesis (the whole utterance so far, not a delta).
	Accept(ctx context.Context, pcm []float32) (string, error)
	// Finish flushes the decoder and returns the final result.
	Finish(ctx context.Context) (Result, error)
	Close() error
}

// ErrNotStreaming is returned by Streamer.NewStream for an offline model.
var ErrNotStreaming = errors.New("model is not streaming")

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes an engine available by name; it panics on duplicates
// because that is a wiring bug.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := factories[name]; ok {
		panic("engine: duplicate registration " + name)
	}
	factories[name] = f
}

func Lookup(name string) (Factory, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := factories[name]
	if !ok {
		return nil, fmt.Errorf("engine %q not compiled in (have %v)", name, namesLocked())
	}
	return f, nil
}

func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	return namesLocked()
}

func namesLocked() []string {
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
