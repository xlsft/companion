// Package displays picks the display backend for the running session and
// wraps it in a displays.Tracker. It lives under internal/platform because
// choosing a backend is platform selection (ADR-0001 rule 3); the domain
// package internal/displays stays free of OS bridges.
package displays

import (
	"context"

	"github.com/xlsft/companion/internal/displays"
	waylanddisplays "github.com/xlsft/companion/internal/platform/linuxwayland/displays"
	x11displays "github.com/xlsft/companion/internal/platform/linuxx11/displays"
	"github.com/xlsft/companion/pkg/contracts"
)

// New starts a Tracker for the session in report. The caller owns it and
// must Close it.
func New(ctx context.Context, report contracts.CapabilityReport) (*displays.Tracker, error) {
	var b displays.Backend
	var err error
	switch report.Session {
	case contracts.SessionWayland:
		b, err = waylanddisplays.Open(ctx)
	case contracts.SessionX11:
		b, err = x11displays.Open()
	default:
		return nil, contracts.Unsupported(contracts.CapScreenCapture)
	}
	if err != nil {
		return nil, err
	}
	return displays.NewTracker(ctx, b, 0)
}
