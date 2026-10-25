package tts

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"

	"github.com/hajimehoshi/go-mp3"
)

// Compatible speech servers do not all speak raw PCM: some only return MP3
// (Mistral's Voxtral through OpenRouter), some send WAV whatever was asked,
// and sample rates vary. These readers turn any of them into the
// PCM24kMono the player expects, as the audio arrives.

// bodyKind is what a response body holds, from its Content-Type.
type bodyKind int

const (
	bodyPCM bodyKind = iota
	bodyMP3
	bodyWAV
)

func kindOf(contentType string, asked bodyKind) bodyKind {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "mpeg"), strings.Contains(ct, "mp3"):
		return bodyMP3
	case strings.Contains(ct, "wav"), strings.Contains(ct, "wave"):
		return bodyWAV
	case strings.Contains(ct, "pcm"), strings.Contains(ct, "l16"):
		return bodyPCM
	}
	return asked // application/octet-stream and friends: trust the request
}

// toPCM24k wraps body so it reads as PCM24kMono.
func toPCM24k(body io.ReadCloser, kind bodyKind) io.ReadCloser {
	switch kind {
	case bodyMP3:
		return &mp3Reader{src: body}
	case bodyWAV:
		return &wavReader{src: body}
	}
	return body
}

// mp3Reader decodes MP3 into mono 16-bit PCM at 24 kHz.
type mp3Reader struct {
	src io.ReadCloser
	dec *mp3.Decoder
	rs  *resampler
	buf []byte
}

func (m *mp3Reader) Read(p []byte) (int, error) {
	if m.dec == nil {
		d, err := mp3.NewDecoder(m.src)
		if err != nil {
			return 0, err
		}
		m.dec = d
		// go-mp3 always yields 16-bit stereo.
		m.rs = newResampler(d.SampleRate(), 2, d)
	}
	return m.rs.Read(p)
}

func (m *mp3Reader) Close() error { return m.src.Close() }

// wavReader skips a RIFF header and converts the 16-bit PCM after it.
type wavReader struct {
	src io.ReadCloser
	rs  *resampler
}

var errWAV = errors.New("unsupported wav")

func (w *wavReader) Read(p []byte) (int, error) {
	if w.rs == nil {
		rate, ch, err := readWAVHeader(w.src)
		if err != nil {
			return 0, err
		}
		w.rs = newResampler(rate, ch, w.src)
	}
	return w.rs.Read(p)
}

func (w *wavReader) Close() error { return w.src.Close() }

// readWAVHeader reads up to the data chunk and returns its format; streamed
// WAVs carry placeholder sizes, so lengths are not trusted.
func readWAVHeader(r io.Reader) (rate, channels int, err error) {
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return 0, 0, err
	}
	if !bytes.Equal(riff[0:4], []byte("RIFF")) || !bytes.Equal(riff[8:12], []byte("WAVE")) {
		return 0, 0, errWAV
	}
	for range 16 { // a handful of chunks at most before "data"
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return 0, 0, err
		}
		size := binary.LittleEndian.Uint32(hdr[4:])
		switch string(hdr[0:4]) {
		case "fmt ":
			if size < 16 || size > 64 {
				return 0, 0, errWAV
			}
			f := make([]byte, size)
			if _, err := io.ReadFull(r, f); err != nil {
				return 0, 0, err
			}
			if binary.LittleEndian.Uint16(f[0:]) != 1 || binary.LittleEndian.Uint16(f[14:]) != 16 {
				return 0, 0, errWAV // only integer 16-bit PCM
			}
			channels = int(binary.LittleEndian.Uint16(f[2:]))
			rate = int(binary.LittleEndian.Uint32(f[4:]))
		case "data":
			if rate <= 0 || channels < 1 || channels > 8 {
				return 0, 0, errWAV
			}
			return rate, channels, nil
		default:
			if size > 1<<20 {
				return 0, 0, errWAV
			}
			if _, err := io.CopyN(io.Discard, r, int64(size+size%2)); err != nil {
				return 0, 0, err
			}
		}
	}
	return 0, 0, errWAV
}

// resampler reads interleaved 16-bit PCM at rate with ch channels and
// yields mono at 24 kHz, interpolating linearly. Speech needs nothing
// better, and it keeps the first sound early.
type resampler struct {
	src  io.Reader
	rate int
	ch   int
	in   []byte    // raw bytes not yet a whole frame
	mono []float64 // decoded mono samples not yet consumed
	pos  float64   // next output position, in input samples from mono[0]
	eof  error
}

func newResampler(rate, ch int, src io.Reader) *resampler {
	return &resampler{src: src, rate: rate, ch: ch}
}

func (r *resampler) fill() {
	if r.eof != nil {
		return
	}
	buf := make([]byte, 4096)
	n, err := r.src.Read(buf)
	r.in = append(r.in, buf[:n]...)
	frame := 2 * r.ch
	for len(r.in) >= frame {
		sum := 0
		for c := range r.ch {
			sum += int(int16(binary.LittleEndian.Uint16(r.in[2*c:])))
		}
		r.mono = append(r.mono, float64(sum)/float64(r.ch))
		r.in = r.in[frame:]
	}
	if err != nil {
		r.eof = err
	}
}

func (r *resampler) Read(p []byte) (int, error) {
	step := float64(r.rate) / 24000
	n := 0
	for n+2 <= len(p) {
		i := int(r.pos)
		if i+1 >= len(r.mono) {
			if r.eof != nil {
				// The last sample has no neighbour to interpolate with.
				if i < len(r.mono) {
					binary.LittleEndian.PutUint16(p[n:], uint16(clamp16(r.mono[i])))
					n += 2
					r.pos += step
					continue
				}
				break
			}
			if n > 0 {
				break // hand over what is ready rather than block
			}
			r.fill()
			continue
		}
		f := r.pos - float64(i)
		v := r.mono[i]*(1-f) + r.mono[i+1]*f
		binary.LittleEndian.PutUint16(p[n:], uint16(clamp16(v)))
		n += 2
		r.pos += step
	}
	// Drop what is behind the read position.
	if drop := int(r.pos); drop > 0 && drop <= len(r.mono) {
		r.mono = r.mono[drop:]
		r.pos -= float64(drop)
	}
	if n == 0 && r.eof != nil {
		if errors.Is(r.eof, io.EOF) {
			return 0, io.EOF
		}
		return 0, r.eof
	}
	return n, nil
}

func clamp16(v float64) int16 {
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	}
	return int16(v)
}
