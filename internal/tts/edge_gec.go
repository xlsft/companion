package tts

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Sec-MS-GEC, the token every Read-aloud request carries. Version 2 of the
// scheme (edge-tts 7.3, src/edge_tts/drm.py) is "<CS64>-<SHA>", both
// uppercase hex, over the decimal Windows file time rounded down to five
// minutes followed by the trusted client token. The "SHA" is SHA-256 of a
// fixed hidden prefix plus that string; only the compression state after
// the prefix is known, so it is resumed here instead of calling
// crypto/sha256. CS64 is a keyed checksum seeded from the digest. The
// constants are Microsoft's, copied from drm.py; edgeGEC is checked against
// values that drm.py itself produced (edge_test.go).

const (
	edgeWinEpoch = 11644473600 // seconds from 1601-01-01 to 1970-01-01
	edgeTickStep = 300         // the token changes every five minutes
)

var (
	edgeSHAInitial = [8]uint32{
		0x6A09E667, 0xBB67AE85, 0x3C6EF372, 0xA54FF53A, 0x510E527F, 0x9B05688C, 0x1F83D9AB, 0x5BE0CD19,
	}
	edgeSHAState = [8]uint32{
		0xFD9FBEA2, 0x91243CE7, 0xBE499094, 0xCD2E1E0A, 0x6C006F49, 0x04BD35AB, 0xFEB0A7E4, 0x03EFFBD2,
	}
	edgeSHASchedule = [14]uint32{
		0xEB24BF9E, 0x1558691F, 0xA0C09EE2, 0x0CFA32BA, 0xA622EE8C, 0xD1DD9491, 0xC0DD25A0,
		0xF9E70AB6, 0xF0C31573, 0x7058CE4E, 0x7188E606, 0x00E5AB33, 0xD258B060, 0x72766564,
	}
	edgeSHAK = [64]uint32{
		0x428A2F98, 0x71374491, 0xB5C0FBCF, 0xE9B5DBA5, 0x3956C25B, 0x59F111F1, 0x923F82A4, 0xAB1C5ED5,
		0xD807AA98, 0x12835B01, 0x243185BE, 0x550C7DC3, 0x72BE5D74, 0x80DEB1FE, 0x9BDC06A7, 0xC19BF174,
		0xE49B69C1, 0xEFBE4786, 0x0FC19DC6, 0x240CA1CC, 0x2DE92C6F, 0x4A7484AA, 0x5CB0A9DC, 0x76F988DA,
		0x983E5152, 0xA831C66D, 0xB00327C8, 0xBF597FC7, 0xC6E00BF3, 0xD5A79147, 0x06CA6351, 0x14292967,
		0x27B70A85, 0x2E1B2138, 0x4D2C6DFC, 0x53380D13, 0x650A7354, 0x766A0ABB, 0x81C2C92E, 0x92722C85,
		0xA2BFE8A1, 0xA81A664B, 0xC24B8B70, 0xC76C51A3, 0xD192E819, 0xD6990624, 0xF40E3585, 0x106AA070,
		0x19A4C116, 0x1E376C08, 0x2748774C, 0x34B0BCB5, 0x391C0CB3, 0x4ED8AA4A, 0x5B9CCA4F, 0x682E6FF3,
		0x748F82EE, 0x78A5636F, 0x84C87814, 0x8CC70208, 0x90BEFFFA, 0xA4506CEB, 0xBEF9A3F7, 0xC67178F2,
	}
	edgeCS64Coefficients = [14][2]uint32{
		{0xE82E91B1, 0xB32700D0}, {0x44DE24C6, 0x88DFF51D}, {0x3BEB65BC, 0x29514038},
		{0xC89C5933, 0xB328CBDF}, {0x8D1EC211, 0xD405B4D5}, {0x314EF5D5, 0x26E55F3C},
		{0xE92491B1, 0x607801C6}, {0xF0A5B9D5, 0x82B2C1FB}, {0x06064CF3, 0xFA51A3DA},
		{0xDA79FA60, 0x207CC464}, {0xBED20560, 0x3C3C74F3}, {0x89F9C9F8, 0xD2D14B61},
		{0x35FA38C6, 0xBB04A2C1}, {0xCB49E3C6, 0xC5626167},
	}
)

const (
	edgeSHAAdjust       = 0xC9DC06A7 // replaces K[14] in the resumed block
	edgeSHASigma0Adjust = 0x059C0B80
	edgeSHASigma1Adjust = 0x000B92C0
	edgeCS64Factor      = 0x82893933
	edgeCS64Adjust0     = 0xF2F90B1A
	edgeCS64Adjust1     = 0x0B1A0000
)

// edgeGEC returns the Sec-MS-GEC value for t.
func edgeGEC(t time.Time) string {
	secs := t.Unix() + edgeWinEpoch
	secs -= secs % edgeTickStep
	data := []byte(strconv.FormatInt(secs*10_000_000, 10) + edgeTrustedToken) // 100 ns ticks
	digest := edgeSHA(data)
	lo, hi := edgeCS64(data, digest)
	var cs [8]byte
	binary.LittleEndian.PutUint32(cs[:4], lo)
	binary.LittleEndian.PutUint32(cs[4:], hi)
	return strings.ToUpper(hex.EncodeToString(cs[:]) + "-" + hex.EncodeToString(digest[:]))
}

func rotr(v uint32, n uint) uint32 { return v>>n | v<<(32-n) }
func rol16(v uint32) uint32        { return v<<16 | v>>16 }

func sigma0(v uint32) uint32 { return rotr(v, 7) ^ rotr(v, 18) ^ v>>3 }
func sigma1(v uint32) uint32 { return rotr(v, 17) ^ rotr(v, 19) ^ v>>10 }

func expandSchedule(w *[64]uint32, from int) {
	for i := from; i < 64; i++ {
		w[i] = sigma1(w[i-2]) + w[i-7] + sigma0(w[i-15]) + w[i-16]
	}
}

// shaRounds runs SHA-256 rounds first..63 and returns the working registers.
func shaRounds(state [8]uint32, w *[64]uint32, k *[64]uint32, first int) [8]uint32 {
	a, b, c, d, e, f, g, h := state[0], state[1], state[2], state[3], state[4], state[5], state[6], state[7]
	for i := first; i < 64; i++ {
		t1 := h + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + (g ^ (e & (f ^ g))) + k[i] + w[i]
		t2 := (rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) | (c & (a | b)))
		a, b, c, d, e, f, g, h = t1+t2, a, b, c, d+t1, e, f, g
	}
	return [8]uint32{a, b, c, d, e, f, g, h}
}

// edgeSHAInit resumes the hidden first block, whose last seven bytes are
// head (the first seven bytes of the token data).
func edgeSHAInit(head []byte) [8]uint32 {
	var w [64]uint32
	w[14] = uint32(head[0])<<16 | uint32(head[1])<<8 | uint32(head[2])
	w[15] = binary.BigEndian.Uint32(head[3:7])
	for i := 16; i < 30; i++ {
		v := edgeSHASchedule[i-16]
		s1 := sigma1(w[i-2])
		if i == 16 {
			s1 ^= edgeSHASigma1Adjust
		}
		v += s1
		if i-7 >= 14 {
			v += w[i-7]
		}
		if i-15 >= 14 {
			s0 := sigma0(w[i-15])
			if i == 29 {
				s0 ^= edgeSHASigma0Adjust
			}
			v += s0
		}
		w[i] = v
	}
	w[30] = sigma1(w[28]) + w[23] + sigma0(w[15]) + w[14] + edgeSHAAdjust - edgeSHAK[14]
	expandSchedule(&w, 31)
	k := edgeSHAK
	k[14] = edgeSHAAdjust
	work := shaRounds(edgeSHAState, &w, &k, 14)
	var out [8]uint32
	for i := range out {
		out[i] = edgeSHAInitial[i] + work[i]
	}
	return out
}

func edgeSHA(data []byte) [32]byte {
	length := 57 + len(data) // hidden 57-byte prefix; our first seven bytes end its block
	pad := ((55-length)%64 + 64) % 64
	msg := make([]byte, 0, len(data)-7+1+pad+8)
	msg = append(msg, data[7:]...)
	msg = append(msg, 0x80)
	msg = append(msg, make([]byte, pad)...)
	msg = binary.BigEndian.AppendUint64(msg, uint64(length)*8)
	state := edgeSHAInit(data[:7])
	for off := 0; off < len(msg); off += 64 {
		var w [64]uint32
		for i := 0; i < 16; i++ {
			w[i] = binary.BigEndian.Uint32(msg[off+4*i:])
		}
		expandSchedule(&w, 16)
		work := shaRounds(state, &w, &edgeSHAK, 0)
		for i := range state {
			state[i] += work[i]
		}
	}
	var out [32]byte
	for i, v := range state {
		binary.BigEndian.PutUint32(out[4*i:], v)
	}
	return out
}

// edgeCS64 is the keyed checksum half of the token.
func edgeCS64(data []byte, digest [32]byte) (uint32, uint32) {
	keyA := binary.LittleEndian.Uint32(digest[0:]) | 1
	keyB := binary.LittleEndian.Uint32(digest[4:]) | 1
	factors := [2]uint32{keyA * edgeCS64Factor, keyB * edgeCS64Factor}
	var terms, offsets []uint32
	for i, c := range edgeCS64Coefficients {
		terms = append(terms, c[0]*factors[i%2])
		offsets = append(offsets, c[1])
	}
	// Little-endian words of 0x00 || data, zero-padded to an even count.
	buf := append([]byte{0}, data...)
	for len(buf)%4 != 0 {
		buf = append(buf, 0)
	}
	if len(buf)/4%2 != 0 {
		buf = append(buf, 0, 0, 0, 0)
	}
	for i := 0; i < len(buf)/4; i++ {
		word := binary.LittleEndian.Uint32(buf[4*i:])
		var term, offset uint32
		if i%2 == 0 {
			term, offset = word*keyA, rol16(word)*0xEF0569FB
		} else {
			term, offset = word*keyB, rol16(word)*0xC31713DB
		}
		if i == 0 {
			term += edgeCS64Adjust0 * factors[0]
			offset += edgeCS64Adjust1
		}
		terms = append(terms, term)
		offsets = append(offsets, offset)
	}
	var swapAcc, swapLast, state, acc uint32
	for i := 0; i < len(terms); i += 2 {
		x, y := terms[i], terms[i+1]
		q := rol16(offsets[i]+x) * 0x76F50F34
		swapAcc += rol16(q)*0xC3EFEA97 + q
		q = rol16(offsets[i+1]+y) * 0x1881CE3C
		swapLast = rol16(q)*0x35BD1EC9 + q
		swapAcc += swapLast

		state = state*keyA + x
		state = rol16(state) * 0xCF98B111
		state = rol16(state) * 0x87085B9F
		state = rol16(state) * 0x12CEB96D
		state = rol16(state) * 0x257E1D83
		acc += state
		t := state*keyB + y
		t = rol16(t) * 0xA27416F5
		t = rol16(t) * 0xD38396FF
		state = rol16(rol16(t)*0x7C932B89) * 0xBFA49F69
		acc += state
	}
	return swapLast ^ state, swapAcc ^ acc
}
