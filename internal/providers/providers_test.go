package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/secrets"
	"github.com/xlsft/openclick/internal/store"
	"github.com/xlsft/openclick/pkg/contracts"
)

const goodKey = "sk-CANARY-good-key-123"

func newService(t *testing.T, client *http.Client) (*Service, *secrets.Memory) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec := secrets.NewMemory()
	return New(store.NewProviderRepo(db), sec, client), sec
}

// fakeOpenAI checks key and model, then streams "red" for every request.
func fakeOpenAI(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+goodKey {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"bad","code":"invalid_api_key"}}`)
			return
		}
		if body.Model != "good-model" {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"message":"nope","code":"model_not_found"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"red"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func input(base, model, key string) Input {
	return Input{Name: "p", Kind: llm.KindOpenAI, BaseURL: base + "/v1", Model: model, APIKey: key, Timeout: 5 * time.Second}
}

func TestCRUDAndSecrets(t *testing.T) {
	ctx := context.Background()
	svc, sec := newService(t, nil)
	info, err := svc.Create(ctx, input("http://127.0.0.1:9", "m", goodKey))
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasKey || info.Input.APIKey != "" {
		t.Fatalf("info = %+v", info)
	}
	ref := secrets.NewRef("provider", info.ID, "api_key")
	if v, err := sec.Get(ctx, ref); err != nil || string(v) != goodKey {
		t.Fatalf("secret = %q, %v", v, err)
	}
	if rec, _ := svc.repo.Get(ctx, info.ID); strings.Contains(fmt.Sprint(*rec), goodKey) {
		t.Fatal("key in SQLite record")
	}

	// Edit without a key keeps the secret.
	in := input("http://127.0.0.1:9", "m2", "")
	in.Overrides = map[llm.Capability]bool{llm.CapVision: false}
	up, err := svc.Update(ctx, info.ID, in)
	if err != nil || !up.HasKey || up.Input.Model != "m2" || up.Input.Overrides[llm.CapVision] {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if v, _ := sec.Get(ctx, ref); string(v) != goodKey {
		t.Fatal("secret lost on edit")
	}
	// A new key replaces it.
	in.APIKey = "new"
	if _, err := svc.Update(ctx, info.ID, in); err != nil {
		t.Fatal(err)
	}
	if v, _ := sec.Get(ctx, ref); string(v) != "new" {
		t.Fatal("secret not replaced")
	}

	if _, err := svc.Create(ctx, Input{Name: "x"}); !errors.Is(err, llm.ErrInvalidConfig) {
		t.Fatalf("invalid create err = %v", err)
	}
	if err := svc.Delete(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := sec.Get(ctx, ref); !errors.Is(err, contracts.ErrSecretNotFound) {
		t.Fatal("secret survived delete")
	}
	if _, err := svc.Get(ctx, info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

func steps(r TestResult) string {
	var b strings.Builder
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "%s=%s/%s ", s.ID, s.Status, s.Code)
	}
	return b.String()
}

func TestTest(t *testing.T) {
	srv := fakeOpenAI(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()

	tests := []struct {
		name string
		in   Input
		want string
		key  string // expected message key of the first failed step
	}{
		{"happy", input(srv.URL, "good-model", goodKey),
			"connection=ok/ auth_model=ok/ capabilities=ok/ ", ""},
		{"bad key", input(srv.URL, "good-model", "wrong"),
			"connection=ok/ auth_model=failed/auth capabilities=skipped/ ", "provider.error.auth"},
		{"unknown model", input(srv.URL, "other", goodKey),
			"connection=ok/ auth_model=failed/model_not_found capabilities=skipped/ ", "provider.error.model_not_found"},
		{"tls", input(tlsSrv.URL, "good-model", goodKey),
			"connection=failed/tls auth_model=skipped/ capabilities=skipped/ ", "provider.error.tls"},
		{"unreachable", input(closedURL, "good-model", goodKey),
			"connection=failed/refused auth_model=skipped/ capabilities=skipped/ ", "provider.error.refused"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t, nil)
			info, err := svc.Create(context.Background(), tc.in)
			if err != nil {
				t.Fatal(err)
			}
			res, err := svc.Test(context.Background(), info.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := steps(res); got != tc.want {
				t.Fatalf("steps = %s, want %s", got, tc.want)
			}
			for _, s := range res.Steps {
				if s.Status == StatusFailed && s.MessageKey != tc.key {
					t.Errorf("key = %q, want %q", s.MessageKey, tc.key)
				}
			}
			b, _ := json.Marshal(res)
			if strings.Contains(string(b), goodKey) || strings.Contains(string(b), "wrong") {
				t.Error("key leaked into result")
			}
			if tc.name == "happy" {
				if res.Capabilities == nil {
					t.Fatal("no report")
				}
				st, _ := svc.Get(context.Background(), info.ID)
				if st.Stored.Detected == nil || !st.Stored.Detected.Caps[llm.CapVision] {
					t.Errorf("detection not persisted: %+v", st.Stored)
				}
			}
		})
	}
}

func TestClassifyNetwork(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "no such host", Name: "x"}, "dns"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("boom"), "network"},
	}
	for _, tc := range tests {
		if got := classifyNetwork(fmt.Errorf("wrap: %w", tc.err)); got != tc.want {
			t.Errorf("%v = %s, want %s", tc.err, got, tc.want)
		}
	}
}
