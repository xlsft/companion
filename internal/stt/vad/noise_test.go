package vad

import (
	"math"
	"testing"
)

func TestNoFalseStartOnSteadyNoise(t *testing.T) {
	tests := []struct {
		name  string
		audio []int16
	}{
		{"white noise -60", whiteNoise(ms(3000), -60, 1)},
		{"white noise -40", whiteNoise(ms(3000), -40, 2)},
		{"white noise -25 (hiss, zcr gate)", whiteNoise(ms(3000), -25, 3)},
		{"hum -60", lowNoise(ms(4000), -60, 4, nil)},
		{"hum -48", lowNoise(ms(4000), -48, 5, nil)},
		{"hum -40 from first frame", lowNoise(ms(4000), -40, 6, nil)},
		{"hum after silence stays quiet at -50 with margin", concat(silence(ms(500)), lowNoise(ms(4000), -50, 7, nil))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewEnergy(DefaultConfig()).Process(tt.audio); got != nil {
				t.Fatalf("false start: %v", got)
			}
		})
	}
}

// A fan that switches on after a quiet start is first taken for speech; the
// floor must then climb until it is background again.
func TestFloorAdaptsToNewHum(t *testing.T) {
	v := NewEnergy(DefaultConfig())
	events := v.Process(concat(silence(ms(500)), lowNoise(ms(10000), -40, 8, nil)))
	if len(events) != 2 || !events[0].Speech || events[1].Speech {
		t.Fatalf("want onset then end as the floor rises, got %v", events)
	}
	if v.Speech() {
		t.Fatal("hum must not stay speech")
	}
	f, ok := v.Floor()
	if !ok || math.Abs(f-(-40)) > 5 {
		t.Fatalf("floor %.1f, want near -40", f)
	}
}

func TestFloorFallsWhenNoiseStops(t *testing.T) {
	v := NewEnergy(DefaultConfig())
	v.Process(concat(lowNoise(ms(3000), -50, 9, nil), silence(ms(1500))))
	if f, _ := v.Floor(); f > -90 {
		t.Fatalf("floor %.1f should have dropped toward digital silence", f)
	}
}

func TestSpeechLikeDetected(t *testing.T) {
	tests := []struct {
		name  string
		audio []int16
		at    int // ms of expected onset
	}{
		{"quiet room", concat(silence(ms(300)), speechLike(ms(2000), -28, 10)), 300},
		{"over steady hum", concat(lowNoise(ms(1500), -52, 11, nil), speechLike(ms(2000), -25, 12)), 1500},
		{"loud", concat(silence(ms(300)), speechLike(ms(2000), -15, 13)), 300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewEnergy(DefaultConfig()).Process(tt.audio)
			if len(got) == 0 || !got[0].Speech {
				t.Fatalf("no onset: %v", got)
			}
			if d := got[0].At.Milliseconds() - int64(tt.at); d < -100 || d > 300 {
				t.Fatalf("onset at %v, want ~%d ms", got[0].At, tt.at)
			}
			if len(got) != 1 {
				t.Fatalf("syllabic dips must be bridged by the hangover: %v", got)
			}
		})
	}
}

func TestStartsWithSpeech(t *testing.T) {
	// Speech from the very first frame: calibration must not learn it as noise.
	got := NewEnergy(DefaultConfig()).Process(speechLike(ms(2000), -22, 14))
	if len(got) != 1 || !got[0].Speech || got[0].At != 0 {
		t.Fatalf("got %v", got)
	}
}
