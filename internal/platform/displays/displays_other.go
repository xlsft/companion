//go:build !linux

package displays

import (
	"context"

	"github.com/xlsft/openclick/internal/displays"
	"github.com/xlsft/openclick/pkg/contracts"
)

// New is not implemented yet for Windows and macOS.
func New(context.Context, contracts.CapabilityReport) (*displays.Tracker, error) {
	return nil, contracts.Unsupported(contracts.CapScreenCapture)
}
