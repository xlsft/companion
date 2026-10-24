package protocol

import "encoding/binary"

// EncodePCM16 packs samples as little-endian s16.
func EncodePCM16(pcm []int16) []byte {
	b := make([]byte, 2*len(pcm))
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

// DecodePCM16 unpacks little-endian s16; a trailing odd byte is ignored.
func DecodePCM16(b []byte) []int16 {
	pcm := make([]int16, len(b)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return pcm
}

// Float32 converts s16 samples to [-1, 1) as engines expect.
func Float32(pcm []int16) []float32 {
	f := make([]float32, len(pcm))
	for i, s := range pcm {
		f[i] = float32(s) / 32768
	}
	return f
}
