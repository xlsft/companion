package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func pcm16(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func samples16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

func wavFile(rate, ch int, data []byte) []byte {
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(data)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(ch))
	w(uint32(rate))
	w(uint32(rate * ch * 2))
	w(uint16(ch * 2))
	w(uint16(16))
	b.WriteString("LIST")
	w(uint32(4))
	b.WriteString("INFO")
	b.WriteString("data")
	w(uint32(0xffffffff)) // streamed: placeholder size
	b.Write(data)
	return b.Bytes()
}

func TestResampler(t *testing.T) {
	tests := []struct {
		name     string
		rate, ch int
		in       []int16
		want     []int16
	}{
		{"24k mono passes through", 24000, 1, []int16{1, 2, 3, 4}, []int16{1, 2, 3, 4}},
		{"48k halves", 48000, 1, []int16{0, 10, 20, 30, 40, 50}, []int16{0, 20, 40}},
		{"stereo is averaged", 24000, 2, []int16{100, 300, -50, 50}, []int16{200, 0}},
		{"12k doubles by interpolation", 12000, 1, []int16{0, 100}, []int16{0, 50, 100, 100}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := io.ReadAll(newResampler(tt.rate, tt.ch, bytes.NewReader(pcm16(tt.in...))))
			if err != nil {
				t.Fatal(err)
			}
			if g := samples16(got); !equal16(g, tt.want) {
				t.Errorf("got %v, want %v", g, tt.want)
			}
		})
	}
}

func equal16(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWAVToPCM(t *testing.T) {
	tests := []struct {
		name string
		wav  []byte
		want []int16
		bad  bool
	}{
		{"24k mono", wavFile(24000, 1, pcm16(5, 6, 7)), []int16{5, 6, 7}, false},
		{"48k stereo", wavFile(48000, 2, pcm16(10, 30, 0, 0, 40, 60, 0, 0)), []int16{20, 50}, false},
		{"not a wav", []byte("RIFX0000WAVE"), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := io.ReadAll(toPCM24k(io.NopCloser(bytes.NewReader(tt.wav)), bodyWAV))
			if tt.bad {
				if err == nil {
					t.Error("bad wav accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if g := samples16(got); !equal16(g, tt.want) {
				t.Errorf("got %v, want %v", g, tt.want)
			}
		})
	}
}

func TestKindOf(t *testing.T) {
	tests := []struct {
		ct    string
		asked bodyKind
		want  bodyKind
	}{
		{"audio/mpeg", bodyPCM, bodyMP3},
		{"audio/wav", bodyPCM, bodyWAV},
		{"audio/x-wav", bodyMP3, bodyWAV},
		{"audio/pcm", bodyMP3, bodyPCM},
		{"application/octet-stream", bodyMP3, bodyMP3},
		{"", bodyPCM, bodyPCM},
	}
	for _, tt := range tests {
		if got := kindOf(tt.ct, tt.asked); got != tt.want {
			t.Errorf("kindOf(%q, %v) = %v, want %v", tt.ct, tt.asked, got, tt.want)
		}
	}
}

// A server that refuses "pcm" gets an MP3 request instead, which is
// decoded to PCM; the next utterance asks for MP3 straight away.
func TestOpenAIFallsBackToMP3(t *testing.T) {
	tone, err := os.ReadFile("testdata/edge_tone.mp3")
	if err != nil {
		t.Fatal(err)
	}
	var pcmAsked, mp3Asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body openAISpeechBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ResponseFormat == "pcm" {
			pcmAsked.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"only mp3 is supported"}}`))
			return
		}
		mp3Asked.Add(1)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(tone)
	}))
	defer srv.Close()
	p := NewOpenAI(OpenAIConfig{Config: Config{BaseURL: srv.URL, APIKey: testKey}, HTTPClient: srv.Client()})
	for i := range 2 {
		s, err := p.SynthesizeStream(context.Background(), SynthesisRequest{Text: "hello", VoiceID: "v", Format: PCM24kMono})
		if err != nil {
			t.Fatalf("utterance %d: %v", i, err)
		}
		n, err := drain(t, s)
		s.Close()
		if err != io.EOF || n == 0 || n%2 != 0 {
			t.Fatalf("utterance %d: %d bytes, %v", i, n, err)
		}
	}
	if pcmAsked.Load() != 1 || mp3Asked.Load() != 2 {
		t.Errorf("asked pcm %d, mp3 %d times; want 1 and 2", pcmAsked.Load(), mp3Asked.Load())
	}
}

func TestRouterVoices(t *testing.T) {
	raw := []byte(`{"data":[
		{"id":"openai/gpt-4o-mini-tts","supported_voices":["alloy","nova"]},
		{"id":"mistralai/voxtral-mini-tts","supported_voices":[{"id":"v1","name":"Paul"},{"voice_id":"v2"}]},
		{"id":"empty/tts","supported_voices":[]}]}`)
	tests := []struct {
		model string
		want  []Voice
		ok    bool
	}{
		{"openai/gpt-4o-mini-tts", []Voice{{ID: "alloy", Name: "alloy"}, {ID: "nova", Name: "nova"}}, true},
		{"mistralai/voxtral-mini-tts", []Voice{{ID: "v1", Name: "Paul"}, {ID: "v2", Name: "v2"}}, true},
		{"empty/tts", nil, false},
		{"missing/model", nil, false},
	}
	for _, tt := range tests {
		got, ok := routerVoices(raw, tt.model)
		if ok != tt.ok || len(got) != len(tt.want) {
			t.Errorf("%s: %+v %v", tt.model, got, ok)
			continue
		}
		for i := range got {
			if got[i].ID != tt.want[i].ID || got[i].Name != tt.want[i].Name {
				t.Errorf("%s: voice %d = %+v, want %+v", tt.model, i, got[i], tt.want[i])
			}
		}
	}
}
