//go:build whisper

package stt

import (
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/stt/protocol"
)

// TestWhisperSidecarEndToEnd streams jfk.wav in real time through the real
// sidecar and reports latencies. Needs OPENCLICK_WHISPER_MODEL.
func TestWhisperSidecarEndToEnd(t *testing.T) {
	needBinary(t)
	model := os.Getenv("OPENCLICK_WHISPER_MODEL")
	if model == "" {
		t.Skip("OPENCLICK_WHISPER_MODEL not set")
	}
	b, err := os.ReadFile("../../third_party/whisper.cpp/samples/jfk.wav")
	if err != nil {
		t.Skip("sample missing: ", err)
	}
	var pcm []int16
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:]))
		off += 8
		if id == "data" {
			pcm = protocol.DecodePCM16(b[off:min(off+size, len(b))])
			break
		}
		off += size + size%2
	}

	// The sample streams in real time (11 s), longer than testCtx allows.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t0 := time.Now()
	c, err := Start(ctx, Config{
		Path:   sidecarBin,
		Args:   []string{"-engine=whisper"},
		Stderr: os.Stderr,
		Init:   protocol.InitializeParams{ModelPath: model, Language: "auto", Backend: "auto"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	info := c.Info()
	t.Logf("spawn+initialize %v (%s, backend %s)", time.Since(t0), info.RuntimeVersion, info.BackendInUse)

	s, err := c.StartSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	streamStart := time.Now()
	type stamped struct {
		at time.Duration
		p  Partial
	}
	got := make(chan []stamped, 1)
	go func() {
		var ps []stamped
		for p := range s.Partials() {
			ps = append(ps, stamped{time.Since(streamStart), p})
		}
		got <- ps
	}()
	const chunk = 1600 // 100 ms, paced in real time like a microphone
	for i := 0; i < len(pcm); i += chunk {
		if err := s.Write(pcm[i:min(i+chunk, len(pcm))]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(streamStart.Add(time.Duration(i+chunk) * time.Second / protocol.SampleRate)))
	}
	f0 := time.Now()
	final, err := s.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finishLatency := time.Since(f0)
	ps := <-got
	if len(ps) == 0 {
		t.Fatal("no partials")
	}
	t.Logf("partials: %d, first at %v after stream start", len(ps), ps[0].at)
	t.Logf("final: finish→result %v (sidecar decode %v) for %v audio, language %s, %d segments",
		finishLatency, final.DecodeTime, final.Duration, final.Language, len(final.Segments))
	if !strings.Contains(strings.ToLower(final.Text), "country") || final.Language != "en" || !final.Speech {
		t.Fatalf("unexpected final: lang %s, speech %v, %d chars", final.Language, final.Speech, len(final.Text))
	}
}
