package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func newTestOpenAI(baseURL string) Provider {
	return NewOpenAI(OpenAIConfig{Config: Config{BaseURL: baseURL, APIKey: testKey}})
}

func TestOpenAIConformance(t *testing.T) {
	conformance{streamPath: "/audio/speech", newProvider: newTestOpenAI}.run(t)
}

func TestOpenAIRequestShape(t *testing.T) {
	tests := []struct {
		name   string
		format Format
		want   string
	}{
		{"pcm", PCM24kMono, "pcm"},
		{"wav", WAV, "wav"},
		{"mp3", MP3, "mp3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any
			var hdr http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hdr = r.Header.Clone()
				if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" {
					t.Errorf("%s %s", r.Method, r.URL.Path)
				}
				json.NewDecoder(r.Body).Decode(&got)
				w.Write(make([]byte, 480))
			}))
			defer srv.Close()
			p := NewOpenAI(OpenAIConfig{
				// Trailing slash and extra header both must be tolerated.
				Config: Config{BaseURL: srv.URL + "/v1/", APIKey: testKey, Headers: map[string]string{"X-Proxy": "1"}},
				Model:  "tts-1",
			})
			s, err := p.SynthesizeStream(context.Background(), SynthesisRequest{Text: "hello", VoiceID: "nova", Speed: 1.25, Format: tt.format})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := io.Copy(io.Discard, readerOf(s)); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"model": "tts-1", "input": "hello", "voice": "nova", "speed": 1.25, "response_format": tt.want}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("body = %v", got)
			}
			if hdr.Get("Authorization") != "Bearer "+testKey || hdr.Get("X-Proxy") != "1" || hdr.Get("Content-Type") != "application/json" {
				t.Errorf("headers = %v", hdr)
			}
		})
	}
}

func TestOpenAINoKeyNoAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Error("Authorization sent without key")
		}
		w.Write(make([]byte, 2))
	}))
	defer srv.Close()
	s, err := NewOpenAI(OpenAIConfig{Config: Config{BaseURL: srv.URL}}).SynthesizeStream(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	io.Copy(io.Discard, readerOf(s))
}

func TestOpenAIPCMFormatRestricted(t *testing.T) {
	p := newTestOpenAI("http://127.0.0.1:1")
	r := req()
	r.Format = PCM16(16000, 1)
	if _, err := p.SynthesizeStream(context.Background(), r); err == nil {
		t.Fatal("want error for non-24k pcm")
	}
}

func TestOpenAIVoices(t *testing.T) {
	p := NewOpenAI(OpenAIConfig{})
	vs, _ := p.ListVoices(context.Background())
	if len(vs) != 11 || vs[0].ID != "alloy" || vs[10].ID != "verse" {
		t.Errorf("voices = %v", vs)
	}
	custom := NewOpenAI(OpenAIConfig{Voices: []Voice{{ID: "af_heart"}}})
	vs, _ = custom.ListVoices(context.Background())
	if len(vs) != 1 || vs[0].ID != "af_heart" {
		t.Errorf("custom voices = %v", vs)
	}
}

// readerOf adapts a Stream to io.Reader for tests that only care about bytes.
func readerOf(s Stream) io.Reader { return &streamReader{s: s} }

type streamReader struct {
	s   Stream
	buf []byte
}

func (r *streamReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		c, err := r.s.Next(context.Background())
		if err != nil {
			return 0, err
		}
		r.buf = c.Data
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
