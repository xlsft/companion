package tts

import (
	"context"
	"fmt"
	"time"
)

// Provider is the TTS backend contract (PRD §9.6).
type Provider interface {
	ListVoices(ctx context.Context) ([]Voice, error)
	// SynthesizeStream starts synthesis and returns once the response headers
	// arrived. ctx governs the whole stream lifetime, not just the call.
	SynthesizeStream(ctx context.Context, req SynthesisRequest) (Stream, error)
	// Cancel aborts every in-flight stream of this provider.
	Cancel(ctx context.Context) error
}

// Stream delivers audio chunks until io.EOF or an error.
type Stream interface {
	// Next blocks for the next chunk. It returns io.EOF after the last chunk
	// and ctx.Err() if ctx ends first (the stream stays usable). After a
	// terminal error every later call returns the same error.
	Next(ctx context.Context) (Chunk, error)
	// Close aborts the request and releases resources. Idempotent.
	Close() error
}

type Voice struct {
	ID        string
	Name      string
	Languages []string // language codes when the provider reports them; may be empty
}

type Container string

const (
	ContainerPCM16 Container = "pcm16" // raw signed 16-bit little-endian
	ContainerWAV   Container = "wav"
	ContainerMP3   Container = "mp3"
)

// Format describes audio bytes. SampleRate and Channels are meaningful for
// PCM16; for WAV/MP3 they are zero when the stream is self-describing.
type Format struct {
	Container  Container
	SampleRate int
	Channels   int
}

// PCM16 returns a raw PCM16 format.
func PCM16(sampleRate, channels int) Format {
	return Format{Container: ContainerPCM16, SampleRate: sampleRate, Channels: channels}
}

var (
	WAV = Format{Container: ContainerWAV}
	MP3 = Format{Container: ContainerMP3}
	// PCM24kMono is the default playback format (OpenAI "pcm", ElevenLabs pcm_24000).
	PCM24kMono = PCM16(24000, 1)
)

func (f Format) String() string {
	if f.Container == ContainerPCM16 {
		return fmt.Sprintf("pcm16/%dHz/%dch", f.SampleRate, f.Channels)
	}
	return string(f.Container)
}

type Chunk struct {
	Data   []byte
	Format Format
}

const (
	MinSpeed     = 0.5
	MaxSpeed     = 1.5
	DefaultSpeed = 1.0
)

type SynthesisRequest struct {
	Text    string
	VoiceID string
	// Speed multiplier in [MinSpeed, MaxSpeed]; 0 means DefaultSpeed.
	Speed  float64
	Format Format // zero value means PCM24kMono
}

// normalize validates r and fills defaults. Errors never include the text.
func (r SynthesisRequest) normalize() (SynthesisRequest, error) {
	if r.Text == "" {
		return r, badRequest("empty text")
	}
	if r.VoiceID == "" {
		return r, badRequest("empty voice id")
	}
	if r.Speed == 0 {
		r.Speed = DefaultSpeed
	}
	if !(r.Speed >= MinSpeed && r.Speed <= MaxSpeed) { // also rejects NaN
		return r, badRequest(fmt.Sprintf("speed %v outside [%v, %v]", r.Speed, MinSpeed, MaxSpeed))
	}
	if r.Format == (Format{}) {
		r.Format = PCM24kMono
	}
	switch r.Format.Container {
	case ContainerPCM16:
		if r.Format.SampleRate <= 0 || r.Format.Channels <= 0 {
			return r, badRequest("pcm16 needs sample rate and channels")
		}
	case ContainerWAV, ContainerMP3:
	default:
		return r, badRequest("unknown audio container")
	}
	return r, nil
}

// Config is shared by HTTP providers.
type Config struct {
	BaseURL string
	// APIKey is a value resolved by the caller from the secret store.
	APIKey string
	// Timeout bounds connecting and waiting for response headers (and the
	// whole ListVoices call); it does not cap the audio body, which may
	// legitimately stream for minutes. Zero means 30s.
	Timeout time.Duration
	// Headers are extra request headers (e.g. for proxies/local servers).
	Headers map[string]string
}

func (c Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 30 * time.Second
	}
	return c.Timeout
}
