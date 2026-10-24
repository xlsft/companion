package vad

import (
	"math"
	"math/rand"
	"time"
)

// Synthetic PCM generators; nothing audio-like is committed to the repo.

func dbAmp(db float64) float64 { return math.Pow(10, db/20) * 32767 }

// whiteNoise has RMS at the given dBFS and a zero-crossing rate near 0.5.
func whiteNoise(d time.Duration, rmsDB float64, seed int64) []int16 {
	r := rand.New(rand.NewSource(seed))
	out := make([]int16, int(d.Seconds()*rate))
	// Uniform [-1,1] has RMS 1/sqrt(3).
	a := dbAmp(rmsDB) * math.Sqrt(3)
	for i := range out {
		out[i] = int16(a * (2*r.Float64() - 1))
	}
	return out
}

// lowNoise is noise through a one-pole low-pass (fan rumble or a voiced-like
// spectrum, ZCR well below 0.4), normalised to rmsDB before env is applied.
func lowNoise(d time.Duration, rmsDB float64, seed int64, env func(t float64) float64) []int16 {
	r := rand.New(rand.NewSource(seed))
	n := int(d.Seconds() * rate)
	f := make([]float64, n)
	var y, sum float64
	for i := range f {
		y = 0.9*y + (2*r.Float64() - 1)
		f[i] = y
		sum += y * y
	}
	g := dbAmp(rmsDB) / math.Sqrt(sum/float64(n))
	out := make([]int16, n)
	for i := range out {
		e := 1.0
		if env != nil {
			e = env(float64(i) / rate)
		}
		out[i] = int16(f[i] * g * e)
	}
	return out
}

// speechLike is low-passed noise with a 4 Hz syllabic envelope that dips to
// zero, like words with gaps between syllables.
func speechLike(d time.Duration, rmsDB float64, seed int64) []int16 {
	return lowNoise(d, rmsDB+3, seed, func(t float64) float64 {
		return 0.5 * (1 + math.Sin(2*math.Pi*4*t))
	})
}
