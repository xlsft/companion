//go:build !linux

package capture

import (
	"context"

	"github.com/xlsft/companion/pkg/contracts"
)

// New is not implemented yet for Windows and macOS.
func New(context.Context, contracts.CapabilityReport, TopologySource) (*Capturer, error) {
	return nil, contracts.Unsupported(contracts.CapScreenCapture)
}

// NewBackend is not implemented yet for Windows and macOS.
func NewBackend(context.Context, string, TopologySource) (*Capturer, error) {
	return nil, contracts.Unsupported(contracts.CapScreenCapture)
}
