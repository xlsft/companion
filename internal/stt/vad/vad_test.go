package vad

import (
	"math"
	"reflect"
	"testing"
	"time"
)

const rate = 16000

func tone(d time.Duration, dbfs float64) []int16 {
	n := int(d.Seconds() * rate)
	amp := math.Pow(10, dbfs/20) * math.Sqrt2 * 32767 // RMS of a sine is amp/√2
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*220*float64(i)/rate))
	}
	return out
}

func silence(d time.Duration) []int16 { return make([]int16, int(d.Seconds()*rate)) }

func concat(parts ...[]int16) []int16 {
	var out []int16
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestEnergy(t *testing.T) {
	tests := []struct {
		name  string
		audio []int16
		want  []Event
	}{
		{"silence", silence(ms(1000)), nil},
		{"quiet noise below threshold", tone(ms(1000), -60), nil},
		{"click shorter than attack", concat(silence(ms(200)), tone(ms(40), -20), silence(ms(500))), nil},
		{
			"speech then long silence",
			concat(silence(ms(200)), tone(ms(500), -20), silence(ms(600))),
			[]Event{{Speech: true, At: ms(200)}, {Speech: false, At: ms(700)}},
		},
		{
			"pause shorter than hangover stays one run",
			concat(tone(ms(300), -20), silence(ms(200)), tone(ms(300), -20)),
			[]Event{{Speech: true, At: 0}},
		},
		{
			"two runs",
			concat(tone(ms(300), -20), silence(ms(500)), tone(ms(300), -20)),
			[]Event{{Speech: true, At: 0}, {Speech: false, At: ms(300)}, {Speech: true, At: ms(800)}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewEnergy(DefaultConfig())
			if got := v.Process(tt.audio); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// Feeding odd-sized chunks must give the same events as one big buffer.
func TestEnergyChunkingInvariant(t *testing.T) {
	audio := concat(silence(ms(130)), tone(ms(470), -25), silence(ms(800)), tone(ms(90), -25))
	whole := NewEnergy(DefaultConfig()).Process(audio)

	v := NewEnergy(DefaultConfig())
	var chunked []Event
	for i := 0; i < len(audio); i += 333 {
		chunked = append(chunked, v.Process(audio[i:min(i+333, len(audio))])...)
	}
	if !reflect.DeepEqual(whole, chunked) || len(whole) != 3 {
		t.Fatalf("whole %v, chunked %v", whole, chunked)
	}
	if !v.Speech() {
		t.Fatal("should end in speech")
	}
	v.Reset()
	if v.Speech() || v.Process(silence(ms(100))) != nil {
		t.Fatal("reset did not clear state")
	}
}

func TestEnergyThresholdConfigurable(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ThresholdDB = -70
	if got := NewEnergy(cfg).Process(tone(ms(500), -60)); len(got) != 1 || !got[0].Speech {
		t.Fatalf("lower threshold should detect quiet tone, got %v", got)
	}
}

func TestAlways(t *testing.T) {
	var a Always
	if a.Process(nil) != nil || a.Speech() {
		t.Fatal("empty input must not start speech")
	}
	if got := a.Process(silence(ms(20))); len(got) != 1 || !a.Speech() {
		t.Fatalf("got %v", got)
	}
	if a.Process(silence(ms(20))) != nil {
		t.Fatal("only one onset")
	}
}
