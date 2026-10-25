package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/xlsft/companion/internal/llm"
)

func TestListModels(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		status  int
		body    string
		want    []string
		wantErr func(error) bool
	}{
		{name: "sorted and deduped", key: "k", status: 200,
			body: `{"object":"list","data":[{"id":"gpt-b"},{"id":"Gpt-a"},{"id":"gpt-b"},{"id":" "}]}`,
			want: []string{"Gpt-a", "gpt-b"}},
		{name: "bare array", status: 200, body: `[{"id":"m2"},{"id":"m1"}]`, want: []string{"m1", "m2"}},
		{name: "empty", key: "k", status: 200, body: `{"data":[]}`, want: []string{}},
		{name: "auth", key: "bad", status: 401, body: `{"error":{"code":"invalid_api_key"}}`,
			wantErr: func(err error) bool { return errors.Is(err, llm.ErrAuth) }},
		{name: "no listing", key: "k", status: 404, body: `not found`,
			wantErr: func(err error) bool {
				var p *llm.ErrProvider
				return errors.As(err, &p) && p.Status == 404
			}},
		{name: "not a list", key: "k", status: 200, body: `{"models":[]}`,
			wantErr: func(err error) bool { return errors.Is(err, llm.ErrMalformedStream) }},
		{name: "not json", key: "k", status: 200, body: `<html>`,
			wantErr: func(err error) bool { return errors.Is(err, llm.ErrMalformedStream) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantAuth := ""
				if tt.key != "" {
					wantAuth = "Bearer " + tt.key
				}
				if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != wantAuth ||
					r.Header.Get("X-Org") != "acme" {
					http.Error(w, "unexpected request", http.StatusTeapot)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			cfg := llm.ProviderConfig{Kind: llm.KindOpenAI, BaseURL: srv.URL + "/v1/", Headers: map[string]string{"X-Org": "acme"}}
			got, err := llm.ListModels(context.Background(), cfg, tt.key, srv.Client())
			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestListModelsCanceled(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := llm.ListModels(ctx, llm.ProviderConfig{Kind: llm.KindOpenAI, BaseURL: srv.URL}, "", srv.Client())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
