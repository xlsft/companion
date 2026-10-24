package setupcheck

import (
	"context"

	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/pkg/contracts"
)

// ProviderTester is the part of providers.Service the BYOK check uses.
type ProviderTester interface {
	List(ctx context.Context) ([]*providers.Info, error)
	Test(ctx context.Context, id string) (providers.TestResult, error)
}

// BYOK is the StepBYOK check: the provider the wizard edits (the first of
// List) is tested step by step with fixed minimal requests.
type BYOK struct {
	Providers ProviderTester
}

func (b BYOK) Run(ctx context.Context) readiness.Result {
	list, err := b.Providers.List(ctx)
	if err != nil {
		return failed("store_error")
	}
	if len(list) == 0 {
		return failed(CodeNoProvider)
	}
	res, err := b.Providers.Test(ctx, list[0].ID)
	if err != nil {
		if ctx.Err() != nil {
			return failed(CodeCanceled)
		}
		return failed("store_error")
	}
	out := readiness.Result{Status: readiness.Passed, Detail: res.Detail()}
	if !res.OK() {
		out.Status, out.Code = readiness.Failed, "failed"
		for _, s := range res.Steps {
			if s.Status == providers.StatusFailed {
				if s.Code != "" {
					out.Code = s.Code
				}
				break
			}
		}
	}
	return out
}

// Platform is the StepPlatform check: the user has read the capability
// table, and a fresh report confirms what it showed.
type Platform struct {
	Detect func(ctx context.Context) (contracts.CapabilityReport, error)
}

// requiredCaps have no fallback: the table says Companion cannot work
// without them (cap.fallback.microphone, cap.fallback.screen_capture).
var requiredCaps = []contracts.Capability{contracts.CapMicrophone, contracts.CapScreenCapture}

func (p Platform) Run(ctx context.Context) readiness.Result {
	r, err := p.Detect(ctx)
	if err != nil && len(r.Capabilities) == 0 {
		if ctx.Err() != nil {
			return failed(CodeCanceled)
		}
		return failed(CodeDetectFailed)
	}
	return PlatformResult(r)
}

// PlatformResult passes unless a capability without fallback is missing.
// Limited, unprobed and fallback capabilities are what the table explains;
// they do not block setup.
func PlatformResult(r contracts.CapabilityReport) readiness.Result {
	res := readiness.Result{Status: readiness.Passed, Detail: map[string]string{}}
	for _, c := range requiredCaps {
		st := r.State(c).Status
		res.Detail[string(c)] = string(st)
		if st == contracts.StatusUnavailable && res.Status == readiness.Passed {
			res.Status, res.Code = readiness.Failed, readiness.PermUnavailable
		}
	}
	return res
}
