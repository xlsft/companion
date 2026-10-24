//go:build whisper && cgo

package whisper

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/stt/engine"
	"github.com/xlsft/openclick/internal/stt/protocol"
)

// Models are never committed; point OPENCLICK_WHISPER_MODEL at a ggml file
// (e.g. ggml-tiny.bin) to run these tests.
func loadEngine(t *testing.T) (engine.Engine, engine.Info) {
	t.Helper()
	model := os.Getenv("OPENCLICK_WHISPER_MODEL")
	if model == "" {
		t.Skip("OPENCLICK_WHISPER_MODEL not set")
	}
	t0 := time.Now()
	e, info, err := New(engine.Config{ModelPath: model, Language: "auto", Backend: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("model load %v, runtime %s, backend %s, fallback %q", time.Since(t0), info.RuntimeVersion, info.BackendInUse, info.FallbackReason)
	t.Cleanup(func() { _ = e.Close() })
	return e, info
}

func jfk(t *testing.T) []float32 {
	t.Helper()
	b, err := os.ReadFile("../../../../third_party/whisper.cpp/samples/jfk.wav")
	if err != nil {
		t.Skip("whisper.cpp submodule sample missing: ", err)
	}
	// RIFF header, then chunks; take the first "data" chunk (16 kHz s16 mono).
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:]))
		off += 8
		if id == "data" {
			return protocol.Float32(protocol.DecodePCM16(b[off:min(off+size, len(b))]))
		}
		off += size + size%2
	}
	t.Fatal("no data chunk")
	return nil
}

func TestMissingModel(t *testing.T) {
	if _, _, err := New(engine.Config{ModelPath: "/nonexistent/model.bin"}); !errors.Is(err, engine.ErrModelNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestTranscribeJFK(t *testing.T) {
	e, info := loadEngine(t)
	pcm := jfk(t)
	tests := []struct {
		name    string
		opts    engine.Options
		wantLng string
	}{
		{"final auto", engine.Options{Language: "auto"}, "en"},
		{"final explicit", engine.Options{Language: "en"}, "en"},
		{"partial", engine.Options{Language: "en", Partial: true}, "en"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t0 := time.Now()
			res, err := e.Transcribe(context.Background(), pcm, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			d := time.Since(t0)
			audio := time.Duration(len(pcm)) * time.Second / protocol.SampleRate
			t.Logf("%s: %v for %v audio (RTF %.3f), %d segments, backend %s", tt.name, d, audio, d.Seconds()/audio.Seconds(), len(res.Segments), info.BackendInUse)
			if !strings.Contains(strings.ToLower(res.Text), "country") || res.Language != tt.wantLng || len(res.Segments) == 0 {
				t.Fatalf("unexpected result: lang %q, %d segments, %d chars", res.Language, len(res.Segments), len(res.Text))
			}
		})
	}
}

func TestTranscribeCancel(t *testing.T) {
	e, _ := loadEngine(t)
	pcm := jfk(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	t0 := time.Now()
	if _, err := e.Transcribe(ctx, pcm, engine.Options{Language: "en"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	t.Logf("abort took %v", time.Since(t0))
	// The abort flag must not leak into the next decode.
	if _, err := e.Transcribe(context.Background(), pcm[:protocol.SampleRate*2], engine.Options{Language: "en", Partial: true}); err != nil {
		t.Fatal(err)
	}
}
