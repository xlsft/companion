// Command tts is the OC-64 spike: it speaks -text through a streaming TTS
// provider and the playback pipeline, sentence by sentence with prefetch,
// and reports time to first audio, hand-off gaps and total time.
//
// The API key is read from OPENCLICK_TTS_KEY and never printed. -fake serves
// silent PCM from a local server instead of calling a provider, and -null
// plays into a simulated device, so the pipeline can be timed without a key
// or without sound.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/xlsft/openclick/internal/audio/playback"
	paudio "github.com/xlsft/openclick/internal/platform/audio"
	"github.com/xlsft/openclick/internal/speech"
	"github.com/xlsft/openclick/internal/talk"
	"github.com/xlsft/openclick/internal/tts"
)

const keyEnv = "OPENCLICK_TTS_KEY"

func main() {
	provider := flag.String("provider", "openai", "openai or elevenlabs")
	text := flag.String("text", "Hello! This is a streaming speech test. Each sentence starts while the previous one plays.", "text to speak")
	voice := flag.String("voice", "", "voice ID (default: alloy for openai, Rachel for elevenlabs)")
	speed := flag.Float64("speed", 1, "speech rate, 0.5 to 1.5")
	baseURL := flag.String("base-url", "", "provider base URL override (compatible or local servers)")
	device := flag.String("device", "", "output device ID (default: system default)")
	fake := flag.Bool("fake", false, "serve silent audio from a local fake provider instead of the real API")
	null := flag.Bool("null", false, "play into a simulated device instead of the speakers")
	flag.Parse()
	if err := run(*provider, *text, *voice, *speed, *baseURL, *device, *fake, *null); err != nil {
		fmt.Fprintln(os.Stderr, "tts:", err)
		os.Exit(1)
	}
}

func run(name, text, voice string, speed float64, baseURL, device string, fake, null bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	key := os.Getenv(keyEnv)
	if fake {
		srv := httptest.NewServer(fakeProvider())
		defer srv.Close()
		baseURL, key = srv.URL, ""
		if name == "openai" {
			baseURL += "/v1"
		}
	} else if key == "" {
		return fmt.Errorf("set %s to the provider API key (or use -fake)", keyEnv)
	}
	cfg := tts.Config{BaseURL: baseURL, APIKey: key}
	var prov tts.Provider
	switch name {
	case "openai":
		prov = tts.NewOpenAI(tts.OpenAIConfig{Config: cfg})
		if voice == "" {
			voice = "alloy"
		}
	case "elevenlabs":
		prov = tts.NewElevenLabs(tts.ElevenLabsConfig{Config: cfg})
		if voice == "" {
			voice = "21m00Tcm4TlvDq8ikWAM"
		}
	default:
		return fmt.Errorf("unknown provider %q", name)
	}

	var sink playback.Sink = paudio.NewSink()
	if null {
		sink = &playback.FakeSink{Realtime: true}
	}
	sp := &talk.TTSSpeaker{
		Provider: prov,
		Player:   playback.New(sink, playback.WithDevice(device)),
		Voice:    voice,
		Speed:    func() float64 { return speed },
	}

	seg := speech.NewSegmenter()
	segs := append(seg.Push(text), seg.Flush()...)
	fmt.Printf("provider %s, voice %s, speed %.2f, %d segments\n", name, voice, speed, len(segs))

	t0 := time.Now()
	var firstAudio time.Duration
	prevEnd := t0
	for i, s := range segs {
		if i+1 < len(segs) {
			sp.Prefetch(ctx, segs[i+1])
		}
		segStart := time.Now()
		var audible time.Duration
		err := sp.Say(ctx, s, func() {
			audible = time.Since(segStart)
			if firstAudio == 0 {
				firstAudio = time.Since(t0)
			}
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return errors.New("interrupted")
			}
			return fmt.Errorf("segment %d: %w", i+1, err)
		}
		// Segment lengths only: the text itself is user content.
		fmt.Printf("  segment %d (%d chars): audio after %v, gap since previous %v\n",
			i+1, len([]rune(s)), audible.Round(time.Millisecond), (segStart.Sub(prevEnd) + audible).Round(time.Millisecond))
		prevEnd = time.Now()
	}
	fmt.Printf("time to first audio %v, total %v\n", firstAudio.Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))
	return nil
}

// fakeProvider answers both providers' speech endpoints with 1 s of silent
// 24 kHz mono PCM, after a 200 ms "synthesis" delay and in 50 ms chunks.
func fakeProvider() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !(strings.HasSuffix(r.URL.Path, "/audio/speech") || strings.HasSuffix(r.URL.Path, "/stream")) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "audio/pcm")
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 2400) // 50 ms
		for i := range 21 {
			d := 25 * time.Millisecond // faster than real time, like a real provider
			if i == 0 {
				d = 200 * time.Millisecond
			}
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
}
