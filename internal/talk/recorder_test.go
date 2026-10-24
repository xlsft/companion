package talk

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/audio"
	"github.com/xlsft/openclick/internal/session"
	"github.com/xlsft/openclick/internal/stt"
	"github.com/xlsft/openclick/internal/stt/engine/fake"
	"github.com/xlsft/openclick/internal/stt/protocol"
	"github.com/xlsft/openclick/internal/stt/server"
	"github.com/xlsft/openclick/pkg/contracts"
)

func inProcessSTT(t *testing.T) *stt.Client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := server.New(server.Config{EngineName: "fake", Factory: fake.Factory(fake.Options{}), PartialInterval: 100 * time.Millisecond})
	served := make(chan struct{})
	go func() {
		_ = srv.Serve(context.Background(), inR, outW)
		outW.Close()
		close(served)
	}()
	c := stt.NewClient(outR, inW, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if _, err := c.Initialize(ctx, protocol.InitializeParams{Language: "auto", Backend: "cpu"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Close(ctx)
		<-served
	})
	return c
}

func TestMicRecorderTranscribes(t *testing.T) {
	src := &audio.FakeSource{Signal: audio.SignalSine, Amplitude: 0.5, Realtime: true}
	rec := &MicRecorder{Mic: audio.NewCapturer(src), STT: ClientSTT(inProcessSTT(t))}
	var mu sync.Mutex
	var partials int
	emit := func(ev session.Event) {
		if _, ok := ev.(session.TranscriptPartial); ok {
			mu.Lock()
			partials++
			mu.Unlock()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rec.Start(ctx, ref, emit); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	text, err := rec.Finish(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("empty transcript for a recorded tone")
	}
	if src.Active() != 0 {
		t.Error("microphone still open after Finish")
	}
	if _, err := rec.Finish(ctx, ref); err != ErrNotRecording {
		t.Errorf("second Finish: %v", err)
	}
}

func TestMicRecorderCancelReleasesMic(t *testing.T) {
	src := &audio.FakeSource{Signal: audio.SignalSilence, Realtime: true}
	rec := &MicRecorder{Mic: audio.NewCapturer(src), STT: ClientSTT(inProcessSTT(t))}
	ctx, cancel := context.WithCancel(context.Background())
	if err := rec.Start(ctx, contracts.SessionRef{ID: "x", Generation: 9}, func(session.Event) {}); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for src.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if src.Active() != 0 {
		t.Fatal("microphone not released after generation cancel")
	}
}
