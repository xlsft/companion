package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/companion/internal/llm"
)

// modelServer lists one model for goodKey, answers 401 otherwise and
// records the keys it was sent.
type modelServer struct {
	*httptest.Server
	mu   sync.Mutex
	auth []string
}

func newModelServer(t *testing.T) *modelServer {
	m := &modelServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.auth = append(m.auth, r.Header.Get("Authorization")+" "+r.Header.Get("X-Api-Key"))
		m.mu.Unlock()
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+goodKey {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"code":"invalid_api_key"}}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"good-model"}]}`)
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *modelServer) sent() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.auth...)
}

func TestListModels(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, nil)
	saved := newModelServer(t)
	other := newModelServer(t)
	info, err := svc.Create(ctx, input(saved.URL, "m", goodKey))
	if err != nil {
		t.Fatal(err)
	}
	// The other server is on the same loopback IP; only the port differs.
	tests := []struct {
		name    string
		srv     *modelServer
		q       ModelQuery
		want    []string
		wantErr error
		noKey   bool // the stored key must not be sent
	}{
		{name: "typed key", srv: other, q: ModelQuery{Kind: llm.KindOpenAI, BaseURL: other.URL + "/v1", APIKey: goodKey},
			want: []string{"good-model"}},
		{name: "stored key for its own host", srv: saved,
			q:    ModelQuery{Kind: llm.KindOpenAI, BaseURL: saved.URL + "/v1/", ProviderID: info.ID},
			want: []string{"good-model"}},
		{name: "stored key not sent elsewhere", srv: other,
			q: ModelQuery{Kind: llm.KindOpenAI, BaseURL: other.URL + "/v1", ProviderID: info.ID}, wantErr: llm.ErrAuth, noKey: true},
		{name: "stored key not sent to another protocol", srv: saved,
			q: ModelQuery{Kind: llm.KindAnthropic, BaseURL: saved.URL, ProviderID: info.ID}, wantErr: llm.ErrAuth, noKey: true},
		{name: "unknown provider", srv: saved,
			q: ModelQuery{Kind: llm.KindOpenAI, BaseURL: saved.URL + "/v1", ProviderID: "prov_gone"}, wantErr: llm.ErrAuth},
		{name: "typed key wins", srv: saved,
			q: ModelQuery{Kind: llm.KindOpenAI, BaseURL: saved.URL + "/v1", ProviderID: info.ID, APIKey: "sk-wrong"}, wantErr: llm.ErrAuth},
	}
	for _, tt := range tests {
		before := len(tt.srv.sent())
		got, err := svc.ListModels(ctx, tt.q)
		if tt.wantErr != nil {
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("%s: err = %v, want %v", tt.name, err, tt.wantErr)
			}
		} else if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %q, %v", tt.name, got, err)
		}
		if tt.noKey {
			for _, a := range tt.srv.sent()[before:] {
				if strings.Contains(a, goodKey) {
					t.Errorf("%s: stored key sent to another host", tt.name)
				}
			}
		}
	}
}
