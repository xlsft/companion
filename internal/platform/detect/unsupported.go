//go:build !linux

package detect

import (
	"context"
	"runtime"

	"github.com/xlsft/companion/pkg/contracts"
)

// Windows and macOS bridges land later; until then every capability is
// reported as not probed instead of being guessed.
type stubDetector struct{}

func newDetector() contracts.CapabilityDetector { return stubDetector{} }

func (stubDetector) Detect(ctx context.Context) (contracts.CapabilityReport, error) {
	r := contracts.CapabilityReport{
		OS:           runtime.GOOS,
		OSVersion:    osVersion(),
		Facts:        map[string]string{},
		Capabilities: map[contracts.Capability]contracts.CapabilityState{},
	}
	switch runtime.GOOS {
	case "windows":
		r.Session = contracts.SessionWindows
	case "darwin":
		r.Session = contracts.SessionMacOS
	}
	for _, c := range allCapabilities {
		r.Capabilities[c] = contracts.CapabilityState{Status: contracts.StatusUnknown, Reason: "probe not implemented"}
	}
	return r, ctx.Err()
}
