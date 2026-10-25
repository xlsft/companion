package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/xlsft/companion/internal/secrets"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/wizard"
)

const secretText = "TOPSECRET reply text"

func TestFallsBack(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
		code string
	}{
		{"5xx", context.Background(), &tts.Error{Kind: tts.ErrUnavailable, Status: 503}, true, "unavailable_503"},
		{"network", context.Background(), &tts.Error{Kind: tts.ErrUnavailable}, true, "unavailable"},
		{"rate limit", context.Background(), &tts.Error{Kind: tts.ErrRateLimited, Status: 429}, true, "rate_limited_429"},
		{"bad key", context.Background(), &tts.Error{Kind: tts.ErrAuth, Status: 401}, false, "auth_401"},
		{"bad request", context.Background(), fmt.Errorf("%w: empty voice id", tts.ErrBadRequest), false, "bad_request"},
		{"barge-in", context.Background(), context.Canceled, false, "canceled"},
		{"ctx ended", canceled, &tts.Error{Kind: tts.ErrUnavailable}, false, "unavailable"},
		{"unknown", context.Background(), errors.New("boom"), false, "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fallsBack(tt.ctx, tt.err); got != tt.want {
				t.Errorf("fallsBack = %v", got)
			}
			if got := speechErrCode(tt.err); got != tt.code {
				t.Errorf("code = %q, want %q", got, tt.code)
			}
		})
	}
}

func TestEdgeFallback(t *testing.T) {
	tests := []struct {
		name       string
		primaryErr error
		edgeErr    error
		wantEdge   bool
		wantErr    error
	}{
		{"primary works", nil, nil, false, nil},
		{"primary down", &tts.Error{Kind: tts.ErrUnavailable, Status: 502}, nil, true, nil},
		{"primary rate limited", &tts.Error{Kind: tts.ErrRateLimited}, nil, true, nil},
		{"bad key surfaces", &tts.Error{Kind: tts.ErrAuth, Status: 401}, nil, false, tts.ErrAuth},
		{"barge-in", context.Canceled, nil, false, context.Canceled},
		{"edge down too", &tts.Error{Kind: tts.ErrUnavailable}, &tts.Error{Kind: tts.ErrUnavailable}, true, tts.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			primary := &tts.Fake{Err: tt.primaryErr, Chunks: 1}
			edge := &tts.Fake{Err: tt.edgeErr, Chunks: 1}
			f := &edgeFallback{Provider: primary, id: "openai", edge: edge, voice: "de-DE-KatjaNeural",
				log: slog.New(slog.NewTextHandler(&logs, nil))}
			st, err := f.SynthesizeStream(context.Background(), tts.SynthesisRequest{Text: secretText, VoiceID: "nova"})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if st != nil {
				st.Close()
			}
			reqs := edge.Requests()
			if got := len(reqs) > 0; got != tt.wantEdge {
				t.Fatalf("edge used = %v", got)
			}
			if tt.wantEdge {
				if reqs[0].VoiceID != "de-DE-KatjaNeural" || len(reqs) != 1 {
					t.Errorf("edge requests = %+v", reqs)
				}
				if !strings.Contains(logs.String(), "code=") {
					t.Errorf("fallback not logged: %s", logs.String())
				}
			} else if logs.Len() != 0 {
				t.Errorf("logged without falling back: %s", logs.String())
			}
			if strings.Contains(logs.String(), "TOPSECRET") {
				t.Errorf("text in logs: %s", logs.String())
			}
		})
	}
}

func TestEdgeVoiceFollowsLanguage(t *testing.T) {
	tests := []struct {
		speech, ui, want string
	}{
		{"de", "en", "de-DE-KatjaNeural"},
		{"auto", "ru", "ru-RU-SvetlanaNeural"},
		{"", "ja", "ja-JP-NanamiNeural"},
	}
	for _, tt := range tests {
		s := settings.Defaults()
		s.Voice.Language, s.UI.Language = tt.speech, tt.ui
		if got := edgeVoice(s); got != tt.want {
			t.Errorf("speech %q ui %q: %q, want %q", tt.speech, tt.ui, got, tt.want)
		}
	}
	for _, lang := range settings.UILanguages {
		if v := tts.EdgeDefaultVoice(lang); !strings.HasPrefix(v, lang+"-") {
			t.Errorf("UI language %s has no own Edge voice: %s", lang, v)
		}
	}
}

func TestSpeechProvider(t *testing.T) {
	ctx := context.Background()
	sec := secrets.NewMemory()
	if err := sec.Put(ctx, ttsSecret(wizard.TTSElevenLabs), []byte("k")); err != nil {
		t.Fatal(err)
	}
	c := &core{secrets: sec, http: &http.Client{}, log: slog.New(slog.DiscardHandler)}
	tests := []struct {
		name  string
		tts   settings.TTS
		kind  string // "edge" alone or "fallback"
		voice string // fixed Edge voice
		label string
	}{
		{"nothing configured", settings.TTS{}, "edge", "fr-FR-DeniseNeural", "Microsoft Edge"},
		{"key missing", settings.TTS{ProviderID: wizard.TTSOpenAI, Voice: "nova"}, "edge", "fr-FR-DeniseNeural", "Microsoft Edge"},
		{"edge chosen", settings.TTS{ProviderID: ttsEdge, Voice: "fr-FR-HenriNeural"}, "edge", "fr-FR-HenriNeural", "Microsoft Edge · fr-FR-HenriNeural"},
		{"edge default voice", settings.TTS{ProviderID: ttsEdge}, "edge", "fr-FR-DeniseNeural", "Microsoft Edge"},
		{"configured", settings.TTS{ProviderID: wizard.TTSElevenLabs, Voice: "v1"}, "fallback", "fr-FR-DeniseNeural", "ElevenLabs · v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := settings.Defaults()
			s.TTS, s.UI.Language = tt.tts, "fr"
			p, label := c.speechProvider(ctx, s)
			if label != tt.label {
				t.Errorf("label = %q", label)
			}
			switch p := p.(type) {
			case *fixedVoice:
				if tt.kind != "edge" || p.voice != tt.voice {
					t.Errorf("edge voice %q, want %s %q", p.voice, tt.kind, tt.voice)
				}
				if _, ok := p.Provider.(*tts.Edge); !ok {
					t.Errorf("speaks with %T", p.Provider)
				}
			case *edgeFallback:
				if tt.kind != "fallback" || p.voice != tt.voice {
					t.Errorf("fallback voice %q, want %s %q", p.voice, tt.kind, tt.voice)
				}
			default:
				t.Fatalf("provider %T", p)
			}
		})
	}
	s := settings.Defaults()
	s.TTS.ProviderID = ttsEdge
	if _, ok := c.voicesProvider(ctx, s).(*tts.Edge); !ok {
		t.Error("Edge voices need a provider without a key")
	}
}

func TestSaveTTSEdge(t *testing.T) {
	ctx := context.Background()
	svc := newSettings(t)
	sec := secrets.NewMemory()
	if err := saveTTS(ctx, sec, svc, wizard.TTSConfig{Provider: ttsEdge, Voice: "de-DE-KatjaNeural", Speed: 1}, ""); err != nil {
		t.Fatal(err)
	}
	if got := svc.Get().TTS; got.ProviderID != ttsEdge || got.Voice != "de-DE-KatjaNeural" {
		t.Errorf("saved %+v", got)
	}
}
