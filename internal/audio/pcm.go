package audio

import "math"

// MinDBFS is reported for digital silence instead of -Inf so a UI meter and
// JSON encoders never see infinities.
const MinDBFS = -120.0

// Level is the signal level of the most recent frame, for a UI meter.
type Level struct {
	RMS  float64 // dBFS, in [MinDBFS, 0]
	Peak float64 // dBFS, in [MinDBFS, 0]
}

// Downmix averages interleaved frames of the given channel count into mono and
// appends them to dst.
func Downmix(dst, interleaved []float32, channels int) []float32 {
	if channels <= 1 {
		return append(dst, interleaved...)
	}
	inv := 1 / float32(channels)
	for i := 0; i+channels <= len(interleaved); i += channels {
		var s float32
		for _, v := range interleaved[i : i+channels] {
			s += v
		}
		dst = append(dst, s*inv)
	}
	return dst
}

// ToInt16 converts a float sample in [-1, 1] to PCM16, clipping anything
// outside the range (resampler ringing can overshoot full scale slightly).
func ToInt16(x float64) int16 {
	v := math.Round(x * 32767)
	switch {
	case v >= 32767:
		return 32767
	case v <= -32768:
		return -32768
	case v != v: // NaN from a misbehaving driver must not poison the stream
		return 0
	}
	return int16(v)
}

// MeasureLevel computes RMS and peak of a PCM16 frame in dBFS.
func MeasureLevel(samples []int16) Level {
	if len(samples) == 0 {
		return Level{RMS: MinDBFS, Peak: MinDBFS}
	}
	var sum float64
	var peak int
	for _, s := range samples {
		v := int(s)
		sum += float64(v * v)
		if v < 0 {
			v = -v
		}
		peak = max(peak, v)
	}
	rms := math.Sqrt(sum/float64(len(samples))) / 32768
	return Level{RMS: toDBFS(rms), Peak: toDBFS(float64(peak) / 32768)}
}

func toDBFS(amp float64) float64 {
	if amp <= 0 {
		return MinDBFS
	}
	return min(max(20*math.Log10(amp), MinDBFS), 0)
}
