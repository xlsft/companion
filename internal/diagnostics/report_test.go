package diagnostics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/diagnostics/logging"
	"github.com/xlsft/openclick/pkg/contracts"
)

type fakeDetector struct {
	r   contracts.CapabilityReport
	err error
}

func (f fakeDetector) Detect(context.Context) (contracts.CapabilityReport, error) { return f.r, f.err }

type fakeProviders struct {
	p   []ProviderInfo
	err error
}

func (f fakeProviders) DiagnosticProviders(context.Context) ([]ProviderInfo, error) {
	return f.p, f.err
}

type fakeSTT struct {
	s   STTStatus
	err error
}

func (f fakeSTT) DiagnosticSTT(context.Context) (STTStatus, error) { return f.s, f.err }

type fakeTTS struct {
	s   TTSStatus
	err error
}

func (f fakeTTS) DiagnosticTTS(context.Context) (TTSStatus, error) { return f.s, f.err }

var fixedNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func TestCollect(t *testing.T) {
	errs := logging.NewErrorLog(5)
	errs.Add(logging.ErrorEvent{Time: fixedNow, Level: "ERROR", Component: "stt", Code: "CRASH", Message: "died"})
	test := &ProviderTest{At: fixedNow, OK: false, Code: "HTTP_401"}

	tests := []struct {
		name  string
		c     Collector
		check func(t *testing.T, r Report)
	}{
		{
			name: "all sources",
			c: Collector{
				Capabilities: fakeDetector{r: contracts.CapabilityReport{
					OS:    "linux",
					Facts: map[string]string{"k": "Bearer abcdefgh12345678"},
					Capabilities: map[contracts.Capability]contracts.CapabilityState{
						contracts.CapMicrophone: {Status: contracts.StatusAvailable, Reason: "api_key=supersecretvalue1"},
					},
				}},
				Providers: fakeProviders{p: []ProviderInfo{{ID: "p1", Kind: "llm", BaseURL: "https://user:pw@api.example.com:8443/v1?x=1", Model: "m", LastTest: test}}},
				STT:       fakeSTT{s: STTStatus{ModelID: "base", Backend: "cpu", State: "ready"}},
				TTS:       fakeTTS{s: TTSStatus{Provider: "eleven", State: "ready"}},
				Errors:    errs,
				Now:       func() time.Time { return fixedNow },
			},
			check: func(t *testing.T, r Report) {
				if !r.GeneratedAt.Equal(fixedNow) || r.AppVersion == "" {
					t.Errorf("meta %+v", r)
				}
				if len(r.Providers) != 1 || r.Providers[0].BaseHost != "api.example.com" || r.Providers[0].LastTest.Code != "HTTP_401" {
					t.Errorf("providers %+v", r.Providers)
				}
				if r.STT == nil || r.STT.ModelID != "base" || r.TTS == nil || r.TTS.Provider != "eleven" {
					t.Errorf("stt/tts %+v %+v", r.STT, r.TTS)
				}
				if len(r.RecentErrors) != 1 || r.RecentErrors[0].Code != "CRASH" {
					t.Errorf("errors %+v", r.RecentErrors)
				}
				if strings.Contains(r.Capabilities.Facts["k"], "abcdefgh") ||
					strings.Contains(r.Capabilities.Capabilities[contracts.CapMicrophone].Reason, "supersecret") {
					t.Errorf("capabilities not redacted: %+v", r.Capabilities)
				}
				if len(r.SourceErrors) != 0 {
					t.Errorf("source errors %v", r.SourceErrors)
				}
			},
		},
		{
			name: "no sources",
			c:    Collector{Now: func() time.Time { return fixedNow }},
			check: func(t *testing.T, r Report) {
				if r.Providers == nil || r.RecentErrors == nil || r.STT != nil || r.TTS != nil {
					t.Errorf("%+v", r)
				}
			},
		},
		{
			name: "failing sources are recorded",
			c: Collector{
				Capabilities: fakeDetector{err: errors.New("dbus down")},
				Providers:    fakeProviders{err: errors.New("db locked sk-abcdefghijklmnop")},
				STT:          fakeSTT{err: errors.New("x")},
				TTS:          fakeTTS{err: errors.New("y")},
			},
			check: func(t *testing.T, r Report) {
				if len(r.SourceErrors) != 4 {
					t.Fatalf("%v", r.SourceErrors)
				}
				if strings.Contains(strings.Join(r.SourceErrors, " "), "sk-abc") {
					t.Error("leak")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tc.c.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, r)
		})
	}
}

func TestCollectCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Collector{}).Collect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestHostOnly(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "",
		"https://api.openai.com/v1":     "api.openai.com",
		"http://localhost:11434":        "localhost",
		"https://u:p@h.example/x?key=1": "h.example",
		"://bad":                        "invalid",
		"no-scheme-host":                "invalid",
	} {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q)=%q want %q", in, got, want)
		}
	}
}
