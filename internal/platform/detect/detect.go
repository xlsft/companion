// Package detect implements runtime capability detection (OC-13). Probes are
// read-only and must never trigger consent dialogs or prompts.
package detect

import (
	"time"

	"github.com/xlsft/openclick/pkg/contracts"
)

// probeTimeout bounds each individual probe so one hung service cannot stall
// startup or the diagnostics screen.
const probeTimeout = 1500 * time.Millisecond

// New returns the detector for the running OS.
func New() contracts.CapabilityDetector { return newDetector() }

var allCapabilities = []contracts.Capability{
	contracts.CapGlobalHotkey, contracts.CapHotkeyRelease, contracts.CapScreenCapture,
	contracts.CapOverlayClickThrough, contracts.CapOverlayFallbackPanel,
	contracts.CapInputInjection, contracts.CapAccessibility, contracts.CapTextInsertion,
	contracts.CapSecretStore, contracts.CapMicrophone, contracts.CapTray,
}
