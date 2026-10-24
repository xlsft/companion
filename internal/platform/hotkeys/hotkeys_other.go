//go:build !linux

package hotkeys

import (
	"context"

	"github.com/xlsft/openclick/pkg/contracts"
)

// New has no Windows or macOS backend yet; those are separate tickets.
func New(ctx context.Context, report contracts.CapabilityReport) (*Service, error) {
	return nil, contracts.Unsupported(contracts.CapGlobalHotkey)
}

func NewWithSocket(ctx context.Context, report contracts.CapabilityReport, _ string) (*Service, error) {
	return New(ctx, report)
}
