//go:build !(linux || windows || (darwin && cgo))

package tray

import (
	"context"

	"github.com/xlsft/openclick/pkg/contracts"
)

type stubTray struct{}

func newTray() contracts.Tray { return stubTray{} }

func (stubTray) Start(context.Context, string, []contracts.TrayMenuItem, func(string)) error {
	return contracts.Unsupported(contracts.CapTray)
}

func (stubTray) SetItems([]contracts.TrayMenuItem) error {
	return contracts.Unsupported(contracts.CapTray)
}

func (stubTray) Stop() error { return nil }
