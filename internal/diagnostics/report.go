// Package diagnostics aggregates the technical state the Diagnostics screen
// renders (OC-17) and exports it as a local archive (OC-18). Nothing here
// holds user content: prompts, transcripts, screenshots, audio and keys never
// enter a Report.
package diagnostics

import (
	"context"
	"net/url"
	"time"

	"github.com/xlsft/openclick/internal/buildinfo"
	"github.com/xlsft/openclick/internal/diagnostics/logging"
	"github.com/xlsft/openclick/pkg/contracts"
)

// ProviderTest is the outcome of the last "test connection" for a provider.
type ProviderTest struct {
	At   time.Time `json:"at"`
	OK   bool      `json:"ok"`
	Code string    `json:"code,omitempty"` // technical code, e.g. "HTTP_401"
}

// ProviderInfo is what a provider registry hands the collector. BaseURL is
// reduced to its host before it reaches a Report.
type ProviderInfo struct {
	ID       string
	Kind     string // "llm", "tts", ...
	BaseURL  string
	Model    string
	LastTest *ProviderTest
}

// ProviderSummary is the report form of ProviderInfo.
type ProviderSummary struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"`
	BaseHost string        `json:"base_host,omitempty"`
	Model    string        `json:"model,omitempty"`
	LastTest *ProviderTest `json:"last_test,omitempty"`
}

// STTStatus describes the speech-recognition runtime.
type STTStatus struct {
	ModelID string `json:"model_id,omitempty"`
	Backend string `json:"backend,omitempty"` // "cpu", "cuda", "metal", ...
	State   string `json:"state"`             // "ready", "starting", "crashed", "not_installed"
	Reason  string `json:"reason,omitempty"`
}

// TTSStatus describes the speech-synthesis provider.
type TTSStatus struct {
	Provider string `json:"provider,omitempty"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
}

// Report is the complete diagnostics snapshot.
type Report struct {
	GeneratedAt  time.Time                  `json:"generated_at"`
	AppVersion   string                     `json:"app_version"`
	Commit       string                     `json:"commit"`
	Capabilities contracts.CapabilityReport `json:"capabilities"`
	Providers    []ProviderSummary          `json:"providers"`
	STT          *STTStatus                 `json:"stt,omitempty"`
	TTS          *TTSStatus                 `json:"tts,omitempty"`
	RecentErrors []logging.ErrorEvent       `json:"recent_errors"`
	// SourceErrors lists sources that failed while collecting, so a partial
	// report says why it is partial.
	SourceErrors []string `json:"source_errors,omitempty"`
}

// Sources are small interfaces so the collector does not import the LLM, STT
// or TTS packages.
type (
	ProviderSource interface {
		DiagnosticProviders(ctx context.Context) ([]ProviderInfo, error)
	}
	STTSource interface {
		DiagnosticSTT(ctx context.Context) (STTStatus, error)
	}
	TTSSource interface {
		DiagnosticTTS(ctx context.Context) (TTSStatus, error)
	}
	ErrorSource interface {
		Snapshot() []logging.ErrorEvent
	}
)

// Collector builds a Report from injected sources; nil sources are skipped.
type Collector struct {
	Capabilities contracts.CapabilityDetector
	Providers    ProviderSource
	STT          STTSource
	TTS          TTSSource
	Errors       ErrorSource

	Now func() time.Time // defaults to time.Now
}

// Collect gathers a snapshot. A failing source is recorded in SourceErrors
// instead of failing the whole report; only context cancellation aborts.
func (c *Collector) Collect(ctx context.Context) (Report, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	r := Report{
		GeneratedAt:  now().UTC(),
		AppVersion:   buildinfo.Version,
		Commit:       shortCommit(buildinfo.Commit),
		Providers:    []ProviderSummary{},
		RecentErrors: []logging.ErrorEvent{},
	}
	fail := func(src string, err error) {
		r.SourceErrors = append(r.SourceErrors, src+": "+logging.RedactString(err.Error()))
	}

	if c.Capabilities != nil {
		if cr, err := c.Capabilities.Detect(ctx); err != nil {
			fail("capabilities", err)
		} else {
			r.Capabilities = redactCapabilities(cr)
		}
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if c.Providers != nil {
		if ps, err := c.Providers.DiagnosticProviders(ctx); err != nil {
			fail("providers", err)
		} else {
			for _, p := range ps {
				r.Providers = append(r.Providers, summarize(p))
			}
		}
	}
	if c.STT != nil {
		if s, err := c.STT.DiagnosticSTT(ctx); err != nil {
			fail("stt", err)
		} else {
			s.Reason = logging.RedactString(s.Reason)
			r.STT = &s
		}
	}
	if c.TTS != nil {
		if s, err := c.TTS.DiagnosticTTS(ctx); err != nil {
			fail("tts", err)
		} else {
			s.Reason = logging.RedactString(s.Reason)
			r.TTS = &s
		}
	}
	if c.Errors != nil {
		r.RecentErrors = append(r.RecentErrors, c.Errors.Snapshot()...)
	}
	return r, ctx.Err()
}

func summarize(p ProviderInfo) ProviderSummary {
	s := ProviderSummary{ID: p.ID, Kind: p.Kind, BaseHost: hostOnly(p.BaseURL), Model: p.Model}
	if p.LastTest != nil {
		t := *p.LastTest
		t.Code = logging.RedactString(t.Code)
		s.LastTest = &t
	}
	return s
}

// hostOnly drops scheme, userinfo, port, path and query: base URLs may carry
// credentials or tenant identifiers.
func hostOnly(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "invalid"
	}
	return u.Hostname()
}

func redactCapabilities(in contracts.CapabilityReport) contracts.CapabilityReport {
	out := in
	if in.Facts != nil {
		out.Facts = make(map[string]string, len(in.Facts))
		for k, v := range in.Facts {
			out.Facts[k] = logging.RedactString(v)
		}
	}
	if in.Capabilities != nil {
		out.Capabilities = make(map[contracts.Capability]contracts.CapabilityState, len(in.Capabilities))
		for k, v := range in.Capabilities {
			v.Reason = logging.RedactString(v.Reason)
			out.Capabilities[k] = v
		}
	}
	return out
}

// shortCommit keeps a full 40-char sha from looking like a hex secret to the
// export safety pass.
func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
