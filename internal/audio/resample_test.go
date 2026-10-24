package audio

import (
	"math"
	"testing"
)

func sine(rate int, freq, amp float64, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

// steady drops the filter transient at both ends so measurements see only the
// steady-state response.
func steady(x []float64) []float64 {
	const skip = 800 // 50 ms at 16 kHz, far beyond the filter length
	return x[skip : len(x)-skip]
}

func rms(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v * v
	}
	return math.Sqrt(s / float64(len(x)))
}

// goertzelAmp is the amplitude of the freq component of x (Hann-windowed).
func goertzelAmp(x []float64, rate int, freq float64) float64 {
	w := 2 * math.Pi * freq / float64(rate)
	var re, im, wsum float64
	for i, v := range x {
		h := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(len(x)-1))
		re += v * h * math.Cos(w*float64(i))
		im -= v * h * math.Sin(w*float64(i))
		wsum += h
	}
	return 2 * math.Hypot(re, im) / wsum
}

func resampleAll(t *testing.T, in []float32, inRate, outRate int) []float64 {
	t.Helper()
	r, err := NewResampler(inRate, outRate)
	if err != nil {
		t.Fatal(err)
	}
	return r.Process(nil, in)
}

func TestResamplerPreservesInBandTones(t *testing.T) {
	tests := []struct {
		inRate int
		freq   float64
	}{
		{48000, 440}, {48000, 1000}, {48000, 3000}, {48000, 6000},
		{44100, 440}, {44100, 1000}, {44100, 3000}, {44100, 6000},
		{96000, 1000}, {22050, 1000}, {8000, 1000}, {16000, 1000},
	}
	for _, tt := range tests {
		const amp = 0.5
		out := steady(resampleAll(t, sine(tt.inRate, tt.freq, amp, tt.inRate), tt.inRate, SampleRate))
		// Amplitude at the expected frequency: passband ripple under 0.1 dB.
		got := goertzelAmp(out, SampleRate, tt.freq)
		if db := 20 * math.Log10(got/amp); math.Abs(db) > 0.1 {
			t.Errorf("%d Hz -> 16k, %.0f Hz tone: gain %.3f dB", tt.inRate, tt.freq, db)
		}
		// Frequency preserved: virtually all energy sits at freq, so a
		// shifted tone (wrong ratio) would fail here.
		if total := rms(out) * math.Sqrt2; got/total < 0.999 {
			t.Errorf("%d Hz -> 16k, %.0f Hz tone: only %.4f of amplitude at the expected frequency", tt.inRate, tt.freq, got/total)
		}
	}
}

func TestResamplerRejectsAliases(t *testing.T) {
	// Anything above 8 kHz must vanish rather than fold into the speech band.
	const maxAliasDB = -70.0
	for _, inRate := range []int{48000, 44100, 96000} {
		for _, freq := range []float64{8500, 9000, 10000, 12000, 15000, 20000} {
			if freq >= float64(inRate)/2 {
				continue
			}
			out := steady(resampleAll(t, sine(inRate, freq, 1, inRate), inRate, SampleRate))
			if db := 20 * math.Log10(rms(out)*math.Sqrt2); db > maxAliasDB {
				t.Errorf("%d Hz input, %.0f Hz tone leaks at %.1f dBFS (limit %.0f)", inRate, freq, db, maxAliasDB)
			}
		}
	}
}

func TestResamplerOutputLength(t *testing.T) {
	for _, inRate := range []int{8000, 11025, 16000, 22050, 32000, 44100, 48000, 96000} {
		r, err := NewResampler(inRate, SampleRate)
		if err != nil {
			t.Fatal(err)
		}
		out := r.Process(nil, make([]float32, inRate*2))
		// Two seconds in, two seconds out, minus the filter delay.
		want := 2*SampleRate - int(math.Ceil(float64(r.Latency())*SampleRate/float64(inRate)))
		if d := len(out) - want; d < -1 || d > 1 {
			t.Errorf("%d Hz: %d samples out, want %d±1", inRate, len(out), want)
		}
	}
}

func TestResamplerChunkingIsTransparent(t *testing.T) {
	in := sine(44100, 1234, 0.7, 44100)
	whole := resampleAll(t, in, 44100, SampleRate)

	r, _ := NewResampler(44100, SampleRate)
	var chunked []float64
	for i, step := 0, 1; i < len(in); i, step = i+step, step%613+37 {
		chunked = r.Process(chunked, in[i:min(i+step, len(in))])
	}
	if len(chunked) != len(whole) {
		t.Fatalf("chunked %d samples, whole %d", len(chunked), len(whole))
	}
	for i := range whole {
		if math.Abs(whole[i]-chunked[i]) > 1e-12 {
			t.Fatalf("sample %d differs: %v vs %v", i, whole[i], chunked[i])
		}
	}
}

func TestNewResamplerRejectsBadRates(t *testing.T) {
	for _, r := range [][2]int{{0, 16000}, {48000, 0}, {-1, 16000}} {
		if _, err := NewResampler(r[0], r[1]); err == nil {
			t.Errorf("NewResampler(%d, %d) succeeded", r[0], r[1])
		}
	}
}

func BenchmarkResampler48k(b *testing.B) {
	r, _ := NewResampler(48000, SampleRate)
	in := sine(48000, 1000, 0.5, 480)
	var out []float64
	b.SetBytes(int64(len(in) * 4))
	for b.Loop() {
		out = r.Process(out[:0], in)
	}
}
