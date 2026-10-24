package vad

import (
	"testing"
	"time"
)

func TestEndpointer(t *testing.T) {
	cfg := EndpointConfig{EndSilence: ms(800), MaxLeadingSilence: ms(3000), MaxDuration: ms(10000)}
	speech := func(d time.Duration) []int16 { return speechLike(d, -25, 21) }
	tests := []struct {
		name   string
		cfg    EndpointConfig
		audio  []int16
		reason Reason // "" = no endpoint
		minAt  time.Duration
		maxAt  time.Duration
	}{
		{"silence after speech", cfg, concat(silence(ms(300)), speech(ms(1500)), silence(ms(2000))), ReasonSilence, ms(2500), ms(2800)},
		{"short pause is bridged", cfg, concat(speech(ms(800)), silence(ms(500)), speech(ms(800)), silence(ms(300))), "", 0, 0},
		{"pause then more speech then end", cfg, concat(speech(ms(800)), silence(ms(500)), speech(ms(800)), silence(ms(1500))), ReasonSilence, ms(2900), ms(3200)},
		{"leading silence timeout", cfg, silence(ms(4000)), ReasonNoSpeech, ms(3000), ms(3100)},
		{"leading noise timeout", cfg, whiteNoise(ms(4000), -50, 22), ReasonNoSpeech, ms(3000), ms(3100)},
		{"steady noise never starts speech", cfg, lowNoise(ms(4000), -48, 23, nil), ReasonNoSpeech, ms(3000), ms(3100)},
		{"click does not cancel leading timeout", cfg, concat(silence(ms(500)), tone(ms(40), -20), silence(ms(3000))), ReasonNoSpeech, ms(3000), ms(3100)},
		{"max duration while talking", cfg, speech(ms(12000)), ReasonMaxDuration, ms(10000), ms(10100)},
		{"leading timeout not applied once speaking", cfg, concat(silence(ms(2000)), speech(ms(2500))), "", 0, 0},
		{"custom end silence", EndpointConfig{EndSilence: ms(300)}, concat(speech(ms(1000)), silence(ms(600))), ReasonSilence, ms(1300), ms(1500)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewEndpointer(DefaultConfig(), tt.cfg)
			var got []*Endpoint
			// 20 ms chunks like the real stream.
			for i := 0; i < len(tt.audio); i += 320 {
				if _, ep := p.Process(tt.audio[i:min(i+320, len(tt.audio))]); ep != nil {
					got = append(got, ep)
				}
			}
			if tt.reason == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected endpoint %+v", got[0])
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want exactly one endpoint, got %d", len(got))
			}
			if got[0].Reason != tt.reason || got[0].At < tt.minAt || got[0].At > tt.maxAt {
				t.Fatalf("got %+v, want %s in [%v,%v]", got[0], tt.reason, tt.minAt, tt.maxAt)
			}
		})
	}
}

func TestEndpointerDefaults(t *testing.T) {
	d := DefaultEndpointConfig()
	if d.EndSilence != 800*time.Millisecond || d.MinSpeech != 120*time.Millisecond || d.MarginDB != 12 {
		t.Fatalf("defaults drifted: %+v", d)
	}
	// A zero config must behave like the defaults.
	p := NewEndpointer(DefaultConfig(), EndpointConfig{})
	_, ep := p.Process(silence(d.MaxLeadingSilence + ms(100)))
	if ep == nil || ep.Reason != ReasonNoSpeech {
		t.Fatalf("got %+v", ep)
	}
}
