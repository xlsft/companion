//go:build sherpa && cgo

package sherpa

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/stt/engine"
	"github.com/xlsft/companion/internal/stt/protocol"
)

// Models are never committed. OPENCLICK_SHERPA_MODEL points at an
// installed or hand-unpacked model directory; OPENCLICK_SHERPA_WAV at a
// 16 kHz mono WAV (default: the first file in the model's test_wavs, which
// sherpa-onnx release archives ship). Two threads keep the test quiet.
func loadEngine(t *testing.T) (*Engine, engine.Info) {
	t.Helper()
	dir := os.Getenv("OPENCLICK_SHERPA_MODEL")
	if dir == "" {
		t.Skip("OPENCLICK_SHERPA_MODEL not set")
	}
	t0 := time.Now()
	e, info, err := New(engine.Config{ModelPath: dir, Language: "auto", Backend: "cpu", Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("model load %v, runtime %s", time.Since(t0), info.RuntimeVersion)
	t.Cleanup(func() { _ = e.Close() })
	return e.(*Engine), info
}

func wav(t *testing.T) []float32 {
	t.Helper()
	path := os.Getenv("OPENCLICK_SHERPA_WAV")
	if path == "" {
		m, _ := filepath.Glob(filepath.Join(os.Getenv("OPENCLICK_SHERPA_MODEL"), "test_wavs", "*.wav"))
		if len(m) == 0 {
			t.Skip("no test wav")
		}
		path = m[0]
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:]))
		off += 8
		if id == "data" {
			pcm := protocol.Float32(protocol.DecodePCM16(b[off:min(off+size, len(b))]))
			return pcm[:min(len(pcm), 8*protocol.SampleRate)] // a few seconds is enough
		}
		off += size + size%2
	}
	t.Fatal("no data chunk")
	return nil
}

func TestMissingModel(t *testing.T) {
	if _, _, err := New(engine.Config{ModelPath: "/nonexistent/model"}); !errors.Is(err, engine.ErrModelNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestTranscribe(t *testing.T) {
	e, _ := loadEngine(t)
	pcm := wav(t)
	t0 := time.Now()
	res, err := e.Transcribe(context.Background(), pcm, engine.Options{Language: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %v for %.1fs audio, lang %q: %q", e.desc.Type, time.Since(t0), float64(len(pcm))/protocol.SampleRate, res.Language, res.Text)
	if res.Text == "" || len(res.Segments) != 1 {
		t.Fatalf("empty result %+v", res)
	}
}

func TestStream(t *testing.T) {
	e, _ := loadEngine(t)
	st, err := e.NewStream(engine.Options{Language: "auto"})
	if errors.Is(err, engine.ErrNotStreaming) {
		t.Skip("offline model")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pcm := wav(t)
	var partials int
	last := ""
	for i := 0; i < len(pcm); i += 1600 {
		text, err := st.Accept(context.Background(), pcm[i:min(i+1600, len(pcm))])
		if err != nil {
			t.Fatal(err)
		}
		if text != last {
			partials, last = partials+1, text
		}
	}
	res, err := st.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d distinct partials, final %q", partials, res.Text)
	if partials < 2 || res.Text == "" {
		t.Fatalf("partials %d, final %q", partials, res.Text)
	}
}

func TestCancel(t *testing.T) {
	e, _ := loadEngine(t)
	pcm := wav(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Transcribe(ctx, pcm, engine.Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if _, err := e.Transcribe(ctx, pcm, engine.Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(t0); d > 100*time.Millisecond {
		t.Fatalf("cancel took %v", d)
	}
	// The abandoned decode must not break the next one.
	if _, err := e.Transcribe(context.Background(), pcm[:protocol.SampleRate*2], engine.Options{}); err != nil {
		t.Fatal(err)
	}
}
