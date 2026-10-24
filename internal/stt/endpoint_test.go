package stt

import (
	"math"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/stt/engine/fake"
)

// voice is a tone with a 4 Hz envelope, speech-like for the energy VAD.
func voice(d time.Duration) []int16 {
	out := make([]int16, int(d.Seconds()*16000))
	for i := range out {
		t := float64(i) / 16000
		out[i] = int16(8000 * (0.6 + 0.4*math.Sin(2*math.Pi*4*t)) * math.Sin(2*math.Pi*220*t))
	}
	return out
}

func TestSessionEndpoint(t *testing.T) {
	c := inProcess(t, fake.Options{})
	ctx := testCtx(t)
	s, err := c.StartSession(ctx, WithVAD(VADOptions{EndSilence: 300 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(append(voice(time.Second), make([]int16, 16000)...)); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-s.Endpoint():
		if e.Reason != EndpointSilence || e.At < time.Second {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no endpoint")
	}
	// Endpoint followed by finish.
	if f, err := s.Finish(ctx); err != nil || !f.Speech {
		t.Fatalf("finish %+v %v", f, err)
	}
	if _, open := <-s.Endpoint(); open {
		t.Fatal("endpoint channel must close with the session")
	}

	// Finish without an endpoint, and no endpoint without WithVAD.
	s2, err := c.StartSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Write(append(voice(time.Second), make([]int16, 32000)...)); err != nil {
		t.Fatal(err)
	}
	if f, err := s2.Finish(ctx); err != nil || !f.Speech {
		t.Fatalf("finish %+v %v", f, err)
	}
	if _, open := <-s2.Endpoint(); open {
		t.Fatal("unexpected endpoint")
	}
}
