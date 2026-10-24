package tts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func newTestEleven(baseURL string) Provider {
	return NewElevenLabs(ElevenLabsConfig{Config: Config{BaseURL: baseURL, APIKey: testKey}})
}

func TestElevenLabsConformance(t *testing.T) {
	conformance{streamPath: "/v1/text-to-speech/voice1/stream", newProvider: newTestEleven}.run(t)
}

func TestElevenLabsRequestShape(t *testing.T) {
	tests := []struct {
		name      string
		format    Format
		speed     float64
		wantQuery string
		wantSpeed float64
	}{
		{"pcm24k", PCM24kMono, 1, "pcm_24000", 1},
		{"pcm16k", PCM16(16000, 1), 1, "pcm_16000", 1},
		{"mp3", MP3, 1, "mp3_44100_128", 1},
		{"clamped low", PCM24kMono, 0.5, "pcm_24000", 0.7},
		{"clamped high", PCM24kMono, 1.5, "pcm_24000", 1.2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any
			var query, key string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/text-to-speech/a b/stream" {
					t.Errorf("%s %s", r.Method, r.URL.EscapedPath())
				}
				query, key = r.URL.Query().Get("output_format"), r.Header.Get("xi-api-key")
				json.NewDecoder(r.Body).Decode(&got)
				w.Write(make([]byte, 480))
			}))
			defer srv.Close()
			p := NewElevenLabs(ElevenLabsConfig{Config: Config{BaseURL: srv.URL, APIKey: testKey}, ModelID: "m1"})
			s, err := p.SynthesizeStream(context.Background(), SynthesisRequest{Text: "hello", VoiceID: "a b", Speed: tt.speed, Format: tt.format})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			io.Copy(io.Discard, readerOf(s))
			want := map[string]any{"text": "hello", "model_id": "m1", "voice_settings": map[string]any{"speed": tt.wantSpeed}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("body = %v", got)
			}
			if query != tt.wantQuery || key != testKey {
				t.Errorf("query=%q key ok=%v", query, key == testKey)
			}
		})
	}
}

func TestElevenLabsUnsupportedFormats(t *testing.T) {
	p := newTestEleven("http://127.0.0.1:1")
	for _, f := range []Format{WAV, PCM16(48000, 1), PCM16(24000, 2)} {
		r := req()
		r.Format = f
		if _, err := p.SynthesizeStream(context.Background(), r); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%v: err = %v", f, err)
		}
	}
}

func TestElevenLabsListVoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/voices" || r.Header.Get("xi-api-key") != testKey {
			http.Error(w, "nope", 401)
			return
		}
		io.WriteString(w, `{"voices":[
			{"voice_id":"v1","name":"Rachel","verified_languages":[{"language":"en"},{"language":"es"}]},
			{"voice_id":"v2","name":"Bob"}]}`)
	}))
	defer srv.Close()
	vs, err := newTestEleven(srv.URL).ListVoices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Voice{{ID: "v1", Name: "Rachel", Languages: []string{"en", "es"}}, {ID: "v2", Name: "Bob"}}
	if !reflect.DeepEqual(vs, want) {
		t.Errorf("voices = %+v", vs)
	}

	bad := NewElevenLabs(ElevenLabsConfig{Config: Config{BaseURL: srv.URL, APIKey: "wrong-" + testKey}})
	_, err = bad.ListVoices(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Errorf("err = %v", err)
	}
	noSecrets(t, err)
}

func TestElevenLabsListVoicesErrors(t *testing.T) {
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "{not json") }))
	defer garbage.Close()
	if _, err := newTestEleven(garbage.URL).ListVoices(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("garbage: %v", err)
	}

	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer stall.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := newTestEleven(stall.URL).ListVoices(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("stall: %v", err)
	}
}
