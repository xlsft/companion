package bench

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
)

// ReadWAV loads a mono 16 kHz WAV as PCM16. Both 16-bit PCM and 32-bit
// float are accepted (FLEURS ships float). Other layouts are an error:
// the sidecar never resamples and neither does the benchmark, so a
// mismatched corpus must fail loudly instead of skewing the numbers.
//
// peak > 0 scales the clip so its loudest sample reaches that fraction of
// full scale. Some FLEURS clips peak near -55 dBFS, below the sidecar's
// absolute VAD floor, which would measure the corpus gain instead of the
// models; a real microphone path has AGC for the same reason.
func ReadWAV(path string, peak float64) ([]int16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseWAV(b, peak)
}

func ParseWAV(b []byte, peak float64) ([]int16, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	var format, channels, bits uint16
	var rate uint32
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:]))
		off += 8
		end := min(off+size, len(b))
		switch id {
		case "fmt ":
			if end-off < 16 {
				return nil, errors.New("short fmt chunk")
			}
			format = binary.LittleEndian.Uint16(b[off:])
			channels = binary.LittleEndian.Uint16(b[off+2:])
			rate = binary.LittleEndian.Uint32(b[off+4:])
			bits = binary.LittleEndian.Uint16(b[off+14:])
		case "data":
			if channels != 1 || rate != 16000 {
				return nil, fmt.Errorf("need mono 16 kHz, got %d ch %d Hz", channels, rate)
			}
			return decodeSamples(b[off:end], format, bits, peak)
		}
		off += size + size%2
	}
	return nil, errors.New("no data chunk")
}

func decodeSamples(d []byte, format, bits uint16, peak float64) ([]int16, error) {
	var f []float64
	switch {
	case format == 1 && bits == 16:
		f = make([]float64, len(d)/2)
		for i := range f {
			f[i] = float64(int16(binary.LittleEndian.Uint16(d[2*i:]))) / 32768
		}
	case format == 3 && bits == 32:
		f = make([]float64, len(d)/4)
		for i := range f {
			f[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(d[4*i:])))
		}
	default:
		return nil, fmt.Errorf("unsupported wav format %d/%d-bit", format, bits)
	}
	gain := 1.0
	if peak > 0 {
		var m float64
		for _, v := range f {
			m = math.Max(m, math.Abs(v))
		}
		if m > 0 {
			gain = peak / m
		}
	}
	out := make([]int16, len(f))
	for i, v := range f {
		out[i] = int16(math.Max(-32768, math.Min(32767, math.Round(v*gain*32768))))
	}
	return out, nil
}
