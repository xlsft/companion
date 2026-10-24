//go:build !linux && !windows && !darwin

package appearance

import (
	"context"

	"github.com/xlsft/companion/pkg/contracts"
)

// New returns a source that reports Unsupported; the theme stays light.
func New() contracts.AppearanceSource { return &otherSource{state: newState()} }

type otherSource struct{ *state }

func (*otherSource) Start(context.Context) error {
	return contracts.Unsupported(contracts.CapColorScheme)
}
