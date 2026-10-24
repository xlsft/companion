package dictation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/stt/protocol"
)

type fakeSession struct {
	mu        sync.Mutex
	samples   int
	cancelled bool
	text      string
}

func (f *fakeSession) Write(pcm []int16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.samples += len(pcm)
	return nil
}

func (f *fakeSession) Finish(context.Context) (stt.Final, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return stt.Final{Text: f.text, Speech: f.text != ""}, nil
}

func (f *fakeSession) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = true
}

func TestMicRecorder(t *testing.T) {
	tests := []struct {
		name     string
		src      *audio.FakeSource
		device   string
		lang     string
		sttErr   error
		startErr error // matched with errors.Is
		wantLang string
	}{
		{name: "records and transcribes", src: &audio.FakeSource{Realtime: true}, lang: "ru", wantLang: "ru"},
		{name: "auto language left to the sidecar", src: &audio.FakeSource{Realtime: true}},
		{name: "unknown microphone", src: &audio.FakeSource{}, device: "gone", startErr: ErrMicrophone},
		{name: "no recogniser", src: &audio.FakeSource{}, sttErr: errors.New("no model"), startErr: errors.New("")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := &fakeSession{text: "привет"}
			var params protocol.SessionStartParams
			r := &MicRecorder{
				Mic: audio.NewCapturer(tt.src),
				STT: func(_ context.Context, opts ...stt.SessionOption) (STTSession, error) {
					for _, o := range opts {
						o(&params)
					}
					if tt.sttErr != nil {
						return nil, tt.sttErr
					}
					return sess, nil
				},
				Device:   func() string { return tt.device },
				Language: func() string { return tt.lang },
			}
			ctx := context.Background()
			err := r.Start(ctx)
			if tt.startErr != nil {
				if err == nil || (errors.Is(tt.startErr, ErrMicrophone) && !errors.Is(err, ErrMicrophone)) {
					t.Fatalf("Start = %v, want %v", err, tt.startErr)
				}
				if tt.sttErr == nil && !sess.cancelled {
					t.Error("recognition session left open")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if params.Language != tt.wantLang || params.Partials || params.VAD != nil {
				t.Errorf("session params %+v", params)
			}
			time.Sleep(60 * time.Millisecond)
			text, err := r.Finish(ctx)
			if err != nil || text != "привет" {
				t.Fatalf("Finish = %q, %v", text, err)
			}
			if sess.samples == 0 {
				t.Error("no audio reached the recogniser")
			}
			if tt.src.Active() != 0 {
				t.Error("microphone left open")
			}
			if _, err := r.Finish(ctx); err == nil {
				t.Error("second Finish succeeded")
			}
		})
	}
}

func TestMicRecorderCancel(t *testing.T) {
	src := &audio.FakeSource{Realtime: true}
	sess := &fakeSession{}
	r := &MicRecorder{Mic: audio.NewCapturer(src), STT: func(context.Context, ...stt.SessionOption) (STTSession, error) { return sess, nil }}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.Cancel()
	deadline := time.Now().Add(time.Second)
	for src.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if src.Active() != 0 || !sess.cancelled {
		t.Errorf("active %d, session cancelled %v", src.Active(), sess.cancelled)
	}
}
