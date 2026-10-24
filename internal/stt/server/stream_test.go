package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/stt/engine/fake"
	"github.com/xlsft/companion/internal/stt/protocol"
)

// A streaming engine decodes every chunk; partials report its hypothesis
// and the final comes from the stream, covering all audio.
func TestStreamingSession(t *testing.T) {
	tests := []struct {
		name     string
		partials bool
		pcm      []int16
		speech   bool
	}{
		{"speech with partials", true, append(make([]int16, 8000), tone(2*time.Second)...), true},
		{"speech without partials", false, tone(time.Second), true},
		{"silence", true, make([]int16, 16000), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, fake.Options{Streaming: true})
			h.init()
			h.startSession("s", tt.partials)
			h.streamPaced("s", tt.pcm, 2*time.Millisecond)
			resp, notes := h.await(h.request(protocol.MethodSessionFinish, protocol.SessionRef{SessionID: "s"}))
			if resp.Error != nil {
				t.Fatal(resp.Error)
			}
			var final protocol.FinalResult
			if err := resp.DecodeResult(&final); err != nil {
				t.Fatal(err)
			}
			want := ""
			if tt.speech {
				want = fake.Text(false, len(tt.pcm))
			}
			if final.Text != want || final.Speech != tt.speech {
				t.Fatalf("final %+v, want %q", final, want)
			}
			partials := byMethod(notes, protocol.MethodSessionPartial)
			if !tt.partials || !tt.speech {
				if len(partials) != 0 {
					t.Fatalf("got %d partials, want none", len(partials))
				}
				return
			}
			if len(partials) == 0 {
				t.Fatal("no partials")
			}
			for _, n := range partials {
				var p protocol.PartialParams
				_ = n.DecodeParams(&p)
				if !strings.HasPrefix(p.Text, "partial ") {
					t.Fatalf("partial %q is not the stream's hypothesis", p.Text)
				}
			}
		})
	}
}

func TestStreamingCancel(t *testing.T) {
	h := newHarness(t, fake.Options{Streaming: true, Delay: 300 * time.Millisecond})
	h.init()
	h.startSession("s", true)
	h.stream("s", tone(time.Second))
	finish := h.request(protocol.MethodSessionFinish, protocol.SessionRef{SessionID: "s"})
	if err := h.call(protocol.MethodSessionCancel, protocol.SessionRef{SessionID: "s"}, nil); err != nil {
		t.Fatal(err)
	}
	resp, _ := h.await(finish)
	if resp.Error == nil || !errors.Is(resp.Error, protocol.ErrSessionCancelled) {
		t.Fatalf("finish after cancel: %v", resp.Error)
	}
}
