package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/xlsft/companion/internal/llm"
)

func TestListModels(t *testing.T) {
	tests := []struct {
		name    string
		base    string // appended to the server URL
		pages   map[string]string
		status  int
		want    []string
		wantErr func(error) bool
		calls   int
	}{
		{name: "one page", base: "", status: 200,
			pages: map[string]string{"": `{"data":[{"id":"claude-b"},{"id":"claude-a"}],"has_more":false,"last_id":"claude-a"}`},
			want:  []string{"claude-a", "claude-b"}, calls: 1},
		{name: "pages", base: "/v1", status: 200, pages: map[string]string{
			"":   `{"data":[{"id":"m1"}],"has_more":true,"last_id":"m1"}`,
			"m1": `{"data":[{"id":"m2"}],"has_more":true,"last_id":"m2"}`,
			"m2": `{"data":[{"id":"m3"}],"has_more":false,"last_id":"m3"}`,
		}, want: []string{"m1", "m2", "m3"}, calls: 3},
		{name: "repeated cursor stops", status: 200, pages: map[string]string{
			"":   `{"data":[{"id":"m1"}],"has_more":true,"last_id":"m1"}`,
			"m1": `{"data":[{"id":"m1"}],"has_more":true,"last_id":"m1"}`,
		}, want: []string{"m1"}, calls: 2},
		{name: "empty", status: 200, pages: map[string]string{"": `{"data":[],"has_more":false}`}, want: []string{}, calls: 1},
		{name: "auth", status: 401, pages: map[string]string{"": `{"type":"error","error":{"type":"authentication_error"}}`},
			wantErr: func(err error) bool { return errors.Is(err, llm.ErrAuth) }, calls: 1},
		{name: "no listing", status: 404, pages: map[string]string{"": `{}`},
			wantErr: func(err error) bool {
				var p *llm.ErrProvider
				return errors.As(err, &p) && p.Status == 404
			}, calls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				q := r.URL.Query()
				if r.URL.Path != "/v1/models" || q.Get("limit") != fmt.Sprint(pageSize) || r.Header.Get("x-api-key") != "k" ||
					r.Header.Get("anthropic-version") != APIVersion || r.Header.Get("X-Org") != "acme" {
					http.Error(w, "unexpected request", http.StatusTeapot)
					return
				}
				body, ok := tt.pages[q.Get("after_id")]
				if !ok {
					http.Error(w, "unknown cursor", http.StatusTeapot)
					return
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(body))
			}))
			defer srv.Close()
			cfg := llm.ProviderConfig{Kind: llm.KindAnthropic, BaseURL: srv.URL + tt.base, Headers: map[string]string{"X-Org": "acme"}}
			got, err := llm.ListModels(context.Background(), cfg, "k", srv.Client())
			if calls != tt.calls {
				t.Errorf("calls = %d, want %d", calls, tt.calls)
			}
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
