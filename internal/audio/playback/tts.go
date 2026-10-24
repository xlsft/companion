package playback

import (
	"context"
	"fmt"

	"github.com/xlsft/companion/internal/tts"
)

// FromTTS adapts a synthesis stream requested in format f. Only raw PCM16 is
// playable: MP3/WAV decoding is deliberately out of scope, so request PCM
// from providers. It takes ownership of s and closes it on error.
func FromTTS(s tts.Stream, f tts.Format) (ChunkSource, Format, error) {
	if f.Container != tts.ContainerPCM16 || f.SampleRate <= 0 || f.Channels <= 0 {
		s.Close()
		return nil, Format{}, fmt.Errorf("playback: %v is not playable, request PCM16", f)
	}
	return &ttsSource{s: s, f: f}, Format{SampleRate: f.SampleRate, Channels: f.Channels}, nil
}

type ttsSource struct {
	s tts.Stream
	f tts.Format
}

func (t *ttsSource) Next(ctx context.Context) ([]byte, error) {
	c, err := t.s.Next(ctx)
	if err != nil {
		return nil, err
	}
	// A provider that ignores the requested format would otherwise play as
	// noise or at the wrong pitch.
	if c.Format != t.f && c.Format != (tts.Format{}) {
		return nil, fmt.Errorf("playback: stream delivered %v, requested %v", c.Format, t.f)
	}
	return c.Data, nil
}

func (t *ttsSource) Close() error { return t.s.Close() }
