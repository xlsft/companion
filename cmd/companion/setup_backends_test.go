package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/companion/internal/secrets"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/wizard"
)

func TestSaveTTS(t *testing.T) {
	type step struct {
		cfg wizard.TTSConfig
		key string
	}
	tests := []struct {
		name    string
		steps   []step
		want    wizard.TTSConfig
		wantKey map[string]string // provider -> stored key
		wantErr error
	}{
		{name: "first save",
			steps:   []step{{wizard.TTSConfig{Provider: wizard.TTSOpenAI, Voice: "nova", Speed: 1.2}, "k1"}},
			want:    wizard.TTSConfig{Provider: wizard.TTSOpenAI, Voice: "nova", Speed: 1.2, HasKey: true},
			wantKey: map[string]string{wizard.TTSOpenAI: "k1"}},
		{name: "empty key keeps the stored one",
			steps: []step{{wizard.TTSConfig{Provider: wizard.TTSOpenAI, Speed: 1}, "k1"},
				{wizard.TTSConfig{Provider: wizard.TTSOpenAI, Voice: "alloy", Speed: 1}, ""}},
			want:    wizard.TTSConfig{Provider: wizard.TTSOpenAI, Voice: "alloy", Speed: 1, HasKey: true},
			wantKey: map[string]string{wizard.TTSOpenAI: "k1"}},
		{name: "keys are per provider",
			steps: []step{{wizard.TTSConfig{Provider: wizard.TTSOpenAI, Speed: 1}, "k1"},
				{wizard.TTSConfig{Provider: wizard.TTSElevenLabs, Speed: 1}, ""}},
			want:    wizard.TTSConfig{Provider: wizard.TTSElevenLabs, Speed: 1},
			wantKey: map[string]string{wizard.TTSOpenAI: "k1"}},
		{name: "speed clamped",
			steps: []step{{wizard.TTSConfig{Provider: wizard.TTSElevenLabs, Speed: 9}, "k"}},
			want:  wizard.TTSConfig{Provider: wizard.TTSElevenLabs, Speed: tts.MaxSpeed, HasKey: true}},
		{name: "unset speed is the default",
			steps: []step{{wizard.TTSConfig{Provider: wizard.TTSElevenLabs}, "k"}},
			want:  wizard.TTSConfig{Provider: wizard.TTSElevenLabs, Speed: tts.DefaultSpeed, HasKey: true}},
		{name: "own server keeps its address and model",
			steps: []step{{wizard.TTSConfig{Provider: wizard.TTSOpenAI, BaseURL: " http://localhost:8880/v1 ", Model: "kokoro", Voice: "af_heart", Speed: 1}, ""}},
			want:  wizard.TTSConfig{Provider: wizard.TTSOpenAI, BaseURL: "http://localhost:8880/v1", Model: "kokoro", Voice: "af_heart", Speed: 1}},
		{name: "unknown provider",
			steps:   []step{{wizard.TTSConfig{Provider: "acme", Speed: 1}, "k"}},
			wantErr: errUnknownTTS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			svc := newSettings(t)
			sec := secrets.NewMemory()
			var err error
			for _, s := range tt.steps {
				if err = saveTTS(ctx, sec, svc, s.cfg, s.key); err != nil {
					break
				}
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				if got := svc.Get().TTS; got.ProviderID != "" {
					t.Errorf("settings changed on error: %+v", got)
				}
				return
			}
			s := svc.Get()
			if got := ttsConfig(ctx, sec, s); got != tt.want {
				t.Errorf("config = %+v, want %+v", got, tt.want)
			}
			if s.TTS.APIKeyRef != ttsSecret(tt.want.Provider) {
				t.Errorf("key ref = %q", s.TTS.APIKeyRef)
			}
			for prov, want := range tt.wantKey {
				got, err := sec.Get(ctx, ttsSecret(prov))
				if err != nil || string(got) != want {
					t.Errorf("%s key = %q, %v", prov, got, err)
				}
			}
		})
	}
}

func TestTTSConfigNoKey(t *testing.T) {
	s := settings.Settings{}
	s.TTS.ProviderID = wizard.TTSOpenAI
	if got := ttsConfig(context.Background(), secrets.NewMemory(), s); got.HasKey {
		t.Errorf("HasKey without a stored key")
	}
}

// A server of the user's own speaks without a key; OpenAI and ElevenLabs
// do not.
func TestTTSProviderKeyless(t *testing.T) {
	c := &core{secrets: secrets.NewMemory()}
	tests := []struct {
		name string
		tts  settings.TTS
		want bool
	}{
		{"local Kokoro", settings.TTS{ProviderID: wizard.TTSOpenAI, BaseURL: "http://localhost:8880/v1", Model: "kokoro"}, true},
		{"OpenAI needs a key", settings.TTS{ProviderID: wizard.TTSOpenAI}, false},
		{"ElevenLabs needs a key", settings.TTS{ProviderID: wizard.TTSElevenLabs, BaseURL: "http://localhost:1/v1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := settings.Settings{TTS: tt.tts}
			p, label := c.ttsProvider(context.Background(), s)
			if (p != nil) != tt.want {
				t.Fatalf("provider = %v, want built %v", p, tt.want)
			}
			if tt.want && label != "Kokoro" {
				t.Errorf("label = %q", label)
			}
		})
	}
}

func newSettings(t *testing.T) *settings.Service {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := settings.NewService(ctx, store.NewKV(db))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// The voice step lists a speech server's models; the stored key goes only
// to the server it was saved with.
func TestTTSListModels(t *testing.T) {
	const stored = "sk-stored-speech"
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent = append(sent, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"tts-b"},{"id":"tts-a"}]}`))
	}))
	defer srv.Close()
	saved := srv.URL + "/v1"
	// Same server under another name: a different host to the backend.
	other := strings.Replace(saved, "127.0.0.1", "localhost", 1)
	tests := []struct {
		name     string
		provider string
		q        wizard.TTSModelQuery
		wantAuth string
	}{
		{"stored key for the saved server", wizard.TTSOpenAI, wizard.TTSModelQuery{BaseURL: saved + "/"}, "Bearer " + stored},
		{"typed key", wizard.TTSOpenAI, wizard.TTSModelQuery{BaseURL: other, APIKey: "sk-typed"}, "Bearer sk-typed"},
		{"no stored key for another server", wizard.TTSOpenAI, wizard.TTSModelQuery{BaseURL: other}, ""},
		{"no stored key of another provider", wizard.TTSElevenLabs, wizard.TTSModelQuery{BaseURL: saved}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			c := &core{secrets: secrets.NewMemory(), settings: newSettings(t), http: srv.Client()}
			if err := saveTTS(ctx, c.secrets, c.settings, wizard.TTSConfig{Provider: tt.provider, BaseURL: saved, Speed: 1}, stored); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			sent = nil
			mu.Unlock()
			got, err := setupTTS{c}.ListModels(ctx, tt.q)
			if err != nil || !slices.Equal(got, []string{"tts-a", "tts-b"}) {
				t.Fatalf("got %q, %v", got, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(sent) != 1 || sent[0] != tt.wantAuth {
				t.Errorf("auth = %q, want %q", sent, tt.wantAuth)
			}
		})
	}
}
