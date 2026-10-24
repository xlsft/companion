package main

import (
	"context"
	"errors"
	"path/filepath"
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
