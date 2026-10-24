package server

import (
	"math"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/stt/engine/fake"
	"github.com/xlsft/openclick/internal/stt/protocol"
)

// voice is a 220 Hz tone with a 4 Hz syllabic envelope: speech-like enough
// for the energy VAD, with dips far shorter than any hangover used here.
func voice(d time.Duration) []int16 {
	out := make([]int16, int(d.Seconds()*16000))
	for i := range out {
		t := float64(i) / 16000
		env := 0.6 + 0.4*math.Sin(2*math.Pi*4*t)
		out[i] = int16(8000 * env * math.Sin(2*math.Pi*220*t))
	}
	return out
}

func startVAD(h *harness, id string, o *protocol.VADOptions) error {
	return h.call(protocol.MethodSessionStart, protocol.SessionStartParams{
		SessionID: id, SampleRate: 16000, Format: protocol.FormatPCMS16LE, VAD: o,
	}, nil)
}

func endpoints(t *testing.T, notes []*protocol.Message) []protocol.EndpointParams {
	t.Helper()
	var out []protocol.EndpointParams
	for _, n := range byMethod(notes, protocol.MethodSessionEndpoint) {
		var p protocol.EndpointParams
		if err := n.DecodeParams(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func finish(t *testing.T, h *harness, id string) (protocol.FinalResult, []*protocol.Message) {
	t.Helper()
	resp, notes := h.await(h.request(protocol.MethodSessionFinish, protocol.SessionRef{SessionID: id}))
	var final protocol.FinalResult
	if resp.Error != nil || resp.DecodeResult(&final) != nil {
		t.Fatalf("finish: %+v", resp.Error)
	}
	return final, notes
}

func TestEndpointNotification(t *testing.T) {
	short := &protocol.VADOptions{EndSilenceMS: 300, MaxLeadingSilenceMS: 1500}
	tests := []struct {
		name   string
		opts   *protocol.VADOptions
		audio  []int16
		reason string // "" = none expected
		speech bool
	}{
		{"silence after speech", short, append(voice(time.Second), make([]int16, 16000)...), protocol.EndpointSilence, true},
		{"leading silence timeout", short, make([]int16, 32000), protocol.EndpointNoSpeech, false},
		{"max duration", &protocol.VADOptions{MaxDurationMS: 1000, EndSilenceMS: 5000}, voice(1500 * time.Millisecond), protocol.EndpointMaxDuration, true},
		{"no vad options means no endpoint", nil, append(voice(time.Second), make([]int16, 32000)...), "", true},
		{"short pause is bridged", &protocol.VADOptions{EndSilenceMS: 800}, append(append(voice(time.Second), make([]int16, 8000)...), voice(time.Second)...), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, fake.Options{})
			h.init()
			if err := startVAD(h, "s", tt.opts); err != nil {
				t.Fatal(err)
			}
			h.stream("s", tt.audio)
			final, notes := finish(t, h, "s") // finish without waiting for the endpoint works too
			got := endpoints(t, notes)
			if tt.reason == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected endpoint %+v", got)
				}
			} else if len(got) != 1 || got[0].Reason != tt.reason || got[0].SessionID != "s" {
				t.Fatalf("want one %s endpoint, got %+v", tt.reason, got)
			}
			if final.Speech != tt.speech {
				t.Fatalf("speech %v, want %v", final.Speech, tt.speech)
			}
		})
	}
}

// Audio after the endpoint is still accepted (the core may race it) and
// does not produce a second notification; finish afterwards works.
func TestEndpointThenFinish(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.init()
	if err := startVAD(h, "s", &protocol.VADOptions{EndSilenceMS: 300}); err != nil {
		t.Fatal(err)
	}
	audio := append(voice(time.Second), make([]int16, 48000)...) // 3 s of trailing silence
	h.stream("s", audio)
	final, notes := finish(t, h, "s")
	if got := endpoints(t, notes); len(got) != 1 || got[0].Reason != protocol.EndpointSilence {
		t.Fatalf("endpoints %+v", got)
	}
	if !final.Speech || final.Text == "" || final.DurationMS != 4000 {
		t.Fatalf("final %+v", final)
	}
}

func TestSessionStartVADValidation(t *testing.T) {
	bad := []protocol.VADOptions{
		{EndSilenceMS: -1}, {MarginDB: -3}, {MinSpeechMS: 999999}, {MaxDurationMS: -5},
	}
	for _, o := range bad {
		h := newHarness(t, fake.Options{})
		h.init()
		if err := startVAD(h, "s", &o); err == nil || err.(*protocol.Error).Code != protocol.ErrInvalidParams.Code {
			t.Fatalf("%+v: got %v", o, err)
		}
	}
}
