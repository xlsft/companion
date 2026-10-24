package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/xlsft/companion/internal/llm"
	_ "github.com/xlsft/companion/internal/llm/anthropic" // register adapters
	_ "github.com/xlsft/companion/internal/llm/openai"
	"github.com/xlsft/companion/internal/llm/probe"
	"github.com/xlsft/companion/pkg/contracts"
)

// Step identifiers of a provider test, in execution order.
const (
	StepConnection   = "connection"
	StepAuthModel    = "auth_model"
	StepCapabilities = "capabilities"
)

// Status of one step.
type Status string

const (
	StatusOK      Status = "ok"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// StepResult is one step of a test. MessageKey is a translation key for
// the UI; Code and Detail are technical and log-safe (never the API key,
// never a URL with credentials, never response text).
type StepResult struct {
	ID         string
	Status     Status
	MessageKey string
	Code       string
	Detail     string
	Latency    time.Duration
}

// TestResult is the outcome of Service.Test.
type TestResult struct {
	ProviderID string
	Steps      []StepResult
	// Capabilities is the probe report (with manual overrides marked); nil
	// when the capability step did not run.
	Capabilities *probe.Report
}

// OK reports whether no step failed.
func (r TestResult) OK() bool {
	for _, s := range r.Steps {
		if s.Status == StatusFailed {
			return false
		}
	}
	return true
}

// Step returns the step with the given id.
func (r TestResult) Step(id string) StepResult {
	for _, s := range r.Steps {
		if s.ID == id {
			return s
		}
	}
	return StepResult{}
}

const (
	connectTimeout = 10 * time.Second
	modelTimeout   = 30 * time.Second
)

// Test checks provider id step by step: reachability of the base URL,
// authentication plus model with a minimal request, then the capability
// probe, whose results are persisted. A failed step skips the later ones.
func (s *Service) Test(ctx context.Context, id string) (TestResult, error) {
	rec, err := s.repo.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	res := TestResult{ProviderID: id}
	cfg := s.config(rec)
	skipRest := func(from string) {
		for _, sid := range []string{StepConnection, StepAuthModel, StepCapabilities} {
			if order(sid) >= order(from) {
				res.Steps = append(res.Steps, StepResult{ID: sid, Status: StatusSkipped, MessageKey: "provider.skipped"})
			}
		}
	}

	conn := s.checkConnection(ctx, cfg)
	res.Steps = append(res.Steps, conn)
	if conn.Status != StatusOK {
		skipRest(StepAuthModel)
		return res, nil
	}

	// One attempt per request: a wrong key must not cause a retry storm.
	tcfg := cfg
	tcfg.Retry = llm.RetryPolicy{MaxAttempts: 1}
	key, kerr := s.key(ctx, rec.SecretRef)
	if kerr != nil {
		code := "secret_error"
		if errors.Is(kerr, contracts.ErrSecretNotFound) {
			code = "secret_missing"
		}
		res.Steps = append(res.Steps, StepResult{ID: StepAuthModel, Status: StatusFailed,
			MessageKey: "provider.error." + code, Code: code})
		skipRest(StepCapabilities)
		return res, nil
	}
	am, _ := s.checkAuthModel(ctx, tcfg, key)
	res.Steps = append(res.Steps, am)
	if am.Status != StatusOK {
		skipRest(StepCapabilities)
		return res, nil
	}

	// Detection must see what the endpoint really does, so manual
	// overrides are stripped for the run and only marked afterwards.
	pcfg := tcfg
	pcfg.Capabilities = nil
	p, err := llm.New(pcfg, key, s.client)
	if err != nil {
		res.Steps = append(res.Steps, StepResult{ID: StepCapabilities, Status: StatusFailed,
			MessageKey: "provider.error.config", Code: llm.ErrorKind(err)})
		return res, nil
	}
	start := time.Now()
	rep := probe.Probe(ctx, p, nil)
	step := StepResult{ID: StepCapabilities, Status: StatusOK, MessageKey: "provider.capabilities.ok", Latency: time.Since(start)}
	if text, _ := rep.Get(probe.CapText); !text.OK {
		step.Status, step.Code, step.Detail = StatusFailed, text.ErrCode, text.Detail
		step.MessageKey = errorKey(text.ErrCode)
	}
	if st, _ := probe.DecodeStored(rec.Capabilities); st.Overrides != nil {
		rep.ApplyOverrides(st.Overrides)
	}
	if step.Status == StatusOK {
		if err := probe.Save(ctx, s.repo, id, rep); err != nil {
			step.Status, step.Code, step.MessageKey = StatusFailed, "store", "provider.error.store"
		}
	}
	res.Steps = append(res.Steps, step)
	res.Capabilities = &rep
	return res, nil
}

func order(id string) int {
	switch id {
	case StepConnection:
		return 0
	case StepAuthModel:
		return 1
	}
	return 2
}

// errorKey maps a normalized error kind to a UI message key.
func errorKey(code string) string {
	switch code {
	case "auth", "rate_limit", "timeout", "network", "context_length", "malformed_stream":
		return "provider.error." + code
	}
	return "provider.error.unknown"
}

// checkConnection reaches BaseURL without credentials: any HTTP answer,
// even 401 or 404, proves DNS, TLS and TCP work.
func (s *Service) checkConnection(ctx context.Context, cfg llm.ProviderConfig) StepResult {
	r := StepResult{ID: StepConnection}
	u, err := cfg.Endpoint()
	if err != nil {
		r.Status, r.Code, r.MessageKey = StatusFailed, "config", "provider.error.config"
		return r
	}
	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodHead, u.String(), nil)
	if err != nil {
		r.Status, r.Code, r.MessageKey = StatusFailed, "config", "provider.error.config"
		return r
	}
	var hc http.Client
	if s.client != nil {
		hc = *s.client
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	start := time.Now()
	resp, err := hc.Do(req)
	r.Latency = time.Since(start)
	if err != nil {
		code := classifyNetwork(err)
		r.Status, r.Code, r.MessageKey = StatusFailed, code, "provider.error."+code
		// Only the classification: url.Error text repeats the full URL.
		r.Detail = "connection failed: " + code
		return r
	}
	resp.Body.Close()
	r.Status, r.MessageKey = StatusOK, "provider.connection.ok"
	return r
}

// classifyNetwork names the failure class of a transport error.
func classifyNetwork(err error) string {
	var (
		dns     *net.DNSError
		unknown x509.UnknownAuthorityError
		invalid x509.CertificateInvalidError
		host    x509.HostnameError
		verify  *tls.CertificateVerificationError
		record  tls.RecordHeaderError
		ne      net.Error
	)
	switch {
	case errors.As(err, &dns):
		return "dns"
	case errors.As(err, &unknown), errors.As(err, &invalid), errors.As(err, &host),
		errors.As(err, &verify), errors.As(err, &record):
		return "tls"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case strings.Contains(err.Error(), "tls:"), strings.Contains(err.Error(), "x509:"):
		return "tls"
	}
	return "network"
}

// checkAuthModel sends one minimal request; the key travels only in it.
func (s *Service) checkAuthModel(ctx context.Context, cfg llm.ProviderConfig, key string) (StepResult, error) {
	r := StepResult{ID: StepAuthModel}
	p, err := llm.New(cfg, key, s.client)
	if err != nil {
		r.Status, r.Code, r.MessageKey = StatusFailed, "config", "provider.error.config"
		return r, err
	}
	cctx, cancel := context.WithTimeout(ctx, modelTimeout)
	defer cancel()
	start := time.Now()
	stream, err := p.Stream(cctx, llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Part{llm.Text("Reply with: pong")}}},
		Policy:   llm.ModelPolicy{MaxOutputTokens: 16},
	})
	if err == nil {
		defer stream.Close()
		for {
			ev, rerr := stream.Recv()
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					err = rerr
				}
				break
			}
			if f, ok := ev.(llm.Failed); ok {
				err = f.Err
				break
			}
			if _, ok := ev.(llm.Completed); ok {
				break
			}
		}
	}
	r.Latency = time.Since(start)
	if err == nil {
		r.Status, r.MessageKey = StatusOK, "provider.auth_model.ok"
		return r, nil
	}
	r.Status = StatusFailed
	r.Code, r.MessageKey = classifyRequest(err)
	r.Detail = err.Error() // status and provider code only, see llm/errors.go
	return r, err
}

// classifyRequest maps a request error to a technical code and UI key.
func classifyRequest(err error) (code, key string) {
	var pe *llm.ErrProvider
	if errors.As(err, &pe) {
		lc := strings.ToLower(pe.Code)
		if pe.Status == http.StatusNotFound || (pe.Status == http.StatusBadRequest && strings.Contains(lc, "model")) {
			return "model_not_found", "provider.error.model_not_found"
		}
		return "provider", "provider.error.unknown"
	}
	code = llm.ErrorKind(err)
	return code, errorKey(code)
}
