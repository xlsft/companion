// Package stub holds restraint signals for platforms without a probe yet
// (Windows, macOS). They report inactive with a diagnostic reason so the
// monitor never restrains on a guess.
package stub

import (
	"context"
	"errors"

	"github.com/xlsft/openclick/internal/restraint"
)

var ErrNotImplemented = errors.New("not implemented")

type signal string

func (s signal) Name() string                       { return string(s) }
func (signal) Active(context.Context) (bool, error) { return false, ErrNotImplemented }

// Signals returns inactive placeholders for the same names the Linux set uses.
func Signals() []restraint.Signal {
	return []restraint.Signal{signal("screen-share"), signal("call"), signal("presentation")}
}
