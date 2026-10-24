package audio

import (
	"math"
	"slices"
	"testing"
)

func TestToInt16(t *testing.T) {
	tests := []struct {
		in   float64
		want int16
	}{
		{0, 0},
		{1, 32767},
		{-1, -32767},
		{0.5, 16384},
		{1.2, 32767},
		{-1.5, -32768},
		{math.Inf(1), 32767},
		{math.Inf(-1), -32768},
		{math.NaN(), 0},
	}
	for _, tt := range tests {
		if got := ToInt16(tt.in); got != tt.want {
			t.Errorf("ToInt16(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestMeasureLevel(t *testing.T) {
	full := make([]int16, FrameSamples)
	half := make([]int16, FrameSamples)
	for i := range full {
		full[i], half[i] = 32767, -16384
	}
	tests := []struct {
		name      string
		in        []int16
		rms, peak float64
	}{
		{"empty", nil, MinDBFS, MinDBFS},
		{"silence", make([]int16, FrameSamples), MinDBFS, MinDBFS},
		{"full scale dc", full, 0, 0},
		{"half scale dc", half, -6.02, -6.02},
		{"min sample", []int16{-32768}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MeasureLevel(tt.in)
			if math.Abs(got.RMS-tt.rms) > 0.01 || math.Abs(got.Peak-tt.peak) > 0.01 {
				t.Fatalf("MeasureLevel = %+v, want rms %.2f peak %.2f", got, tt.rms, tt.peak)
			}
		})
	}
}

func TestDownmix(t *testing.T) {
	tests := []struct {
		name     string
		in       []float32
		channels int
		want     []float32
	}{
		{"mono passthrough", []float32{0.1, -0.2}, 1, []float32{0.1, -0.2}},
		{"stereo", []float32{1, 0, 0.5, 0.5, -1, 1}, 2, []float32{0.5, 0.5, 0}},
		{"quad", []float32{1, 1, 1, 1, 0, 0, 0, 1}, 4, []float32{1, 0.25}},
		{"partial frame ignored", []float32{1, 1, 1}, 2, []float32{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Downmix(nil, tt.in, tt.channels); !slices.Equal(got, tt.want) {
				t.Fatalf("Downmix = %v, want %v", got, tt.want)
			}
		})
	}
}
