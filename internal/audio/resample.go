package audio

import (
	"fmt"
	"math"
)

// Resampler filter design. The stopband starts exactly at the lower Nyquist
// frequency of the pair: capturing to 16 kHz nothing above 8 kHz folds back
// into the speech band, and playing 24 kHz TTS on a 48 kHz device no spectral
// image above 12 kHz survives. The passband keeps 87.5% of it (7 kHz and
// 10.5 kHz respectively), which covers whisper's and TTS voices' useful range.
const (
	passbandFrac = 0.875
	stopbandDB   = 90.0
	// maxPhases bounds the polyphase table for awkward rate pairs; beyond it
	// the fractional position is quantised to 1/maxPhases of an input sample,
	// far below what the stopband attenuation can resolve anyway.
	maxPhases = 1024
)

// Resampler converts a mono stream between sample rates with a polyphase
// windowed-sinc (Kaiser) low-pass filter. It is stateful and streams: input
// may be fed in chunks of any size. Not safe for concurrent use.
type Resampler struct {
	l, m   int64 // out/in = l/m, reduced
	phases int64
	half   int         // taps on each side of the output instant
	table  [][]float64 // [phase][2*half], each row normalised to unity DC gain
	buf    []float64   // input history; see pos
	pos    int         // buf index of the input sample at or before the next output instant
	phase  int64       // fractional part of the next output instant, in units of 1/l
}

// NewResampler returns a resampler from inRate to outRate (Hz).
func NewResampler(inRate, outRate int) (*Resampler, error) {
	if inRate <= 0 || outRate <= 0 {
		return nil, fmt.Errorf("audio: invalid resample rates %d -> %d", inRate, outRate)
	}
	g := gcd(int64(inRate), int64(outRate))
	r := &Resampler{l: int64(outRate) / g, m: int64(inRate) / g}
	r.phases = min(r.l, maxPhases)

	nyq := 0.5 * float64(min(inRate, outRate))
	cutoff := nyq * (1 + passbandFrac) / 2 / float64(inRate) // cycles per input sample
	transition := nyq * (1 - passbandFrac) / float64(inRate) // normalised width
	// Kaiser design formulas (Oppenheim & Schafer 7.6).
	beta := 0.1102 * (stopbandDB - 8.7)
	taps := int(math.Ceil((stopbandDB - 8) / (2.285 * 2 * math.Pi * transition)))
	r.half = max(taps/2, 2)

	i0b := besselI0(beta)
	r.table = make([][]float64, r.phases)
	for p := range r.table {
		frac := float64(p) / float64(r.phases)
		row := make([]float64, 2*r.half)
		var sum float64
		for k := range row {
			tau := frac + float64(r.half-1-k) // distance from output instant, input samples
			x := tau / float64(r.half)
			if x <= -1 || x >= 1 {
				continue
			}
			w := besselI0(beta*math.Sqrt(1-x*x)) / i0b
			row[k] = 2 * cutoff * sinc(2*cutoff*tau) * w
			sum += row[k]
		}
		for k := range row {
			row[k] /= sum
		}
		r.table[p] = row
	}
	// Pre-pad so the first output (at input instant 0) has a full window.
	r.buf = make([]float64, r.half-1, 4*r.half)
	r.pos = r.half - 1
	return r, nil
}

// Latency is the filter delay in input samples: output for instant t is only
// produced once input up to t+Latency has arrived.
func (r *Resampler) Latency() int { return r.half }

// Process appends the resampled form of src to dst and returns it.
func (r *Resampler) Process(dst []float64, src []float32) []float64 {
	for _, s := range src {
		r.buf = append(r.buf, float64(s))
	}
	n := 2 * r.half
	for r.pos+r.half < len(r.buf) {
		row := r.table[r.phase*r.phases/r.l]
		win := r.buf[r.pos-r.half+1 : r.pos-r.half+1+n]
		var acc float64
		for k, c := range row {
			acc += win[k] * c
		}
		dst = append(dst, acc)
		r.phase += r.m
		r.pos += int(r.phase / r.l)
		r.phase %= r.l
	}
	// Drop history no future window can reach; copy only once it is worth it.
	if drop := r.pos - r.half + 1; drop > 0 && drop >= len(r.buf)/2 {
		drop = min(drop, len(r.buf))
		r.buf = append(r.buf[:0], r.buf[drop:]...)
		r.pos -= drop
	}
	return dst
}

// Flush appends the output still held back by the filter delay, as if the
// input ended in silence, and returns it. Call it once at the end of a
// stream; a playback tail would otherwise lose its last couple of ms.
func (r *Resampler) Flush(dst []float64) []float64 {
	return r.Process(dst, make([]float32, r.half))
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// besselI0 is the zeroth-order modified Bessel function of the first kind,
// by its power series; converges quickly for the beta values used here.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 64; k++ {
		term *= (x / 2) / float64(k)
		sum += term * term
		if term*term < sum*1e-17 {
			break
		}
	}
	return sum
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
