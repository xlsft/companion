//go:build !linux

package insert

import "github.com/xlsft/companion/pkg/contracts"

// Options configure New.
type Options struct {
	TokenPath    string
	UseClipboard func() bool
}

// New has no backend yet on Windows and macOS (SendInput / CGEvent), so
// every insertion reports Unsupported(CapTextInsertion).
func New(contracts.CapabilityReport, Options) *Inserter { return &Inserter{} }
