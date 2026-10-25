package tts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestOpenAIListModels(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		status  int
		body    string
		want    []string
		wantErr error
	}{
		{name: "list", key: testKey, status: 200,
			body: `{"object":"list","data":[{"id":"tts-1"},{"id":"kokoro"},{"id":"tts-1"}]}`, want: []string{"kokoro", "tts-1"}},
		{name: "keyless bare array", status: 200, body: `[{"id":"speaches-ai/Kokoro-82M-v1.0-ONNX"}]`,
			want: []string{"speaches-ai/Kokoro-82M-v1.0-ONNX"}},
		{name: "empty", status: 200, body: `{"data":[]}`, want: []string{}},
		{name: "auth", key: "bad", status: 401, body: `{}`, wantErr: ErrAuth},
		{name: "no listing", status: 404, body: `nope`, wantErr: ErrBadRequest},
		{name: "not a list", status: 200, body: `{"voices":[]}`, wantErr: ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := ""
				if tt.key != "" {
					want = "Bearer " + tt.key
				}
				if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != want ||
					r.Header.Get("X-Proxy") != "1" {
					http.Error(w, "unexpected request", http.StatusTeapot)
					return
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			p := NewOpenAI(OpenAIConfig{Config: Config{BaseURL: srv.URL + "/v1/", APIKey: tt.key, Headers: map[string]string{"X-Proxy": "1"}}})
			got, err := p.ListModels(context.Background())
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
