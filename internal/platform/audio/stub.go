//go:build !cgo

package audio

import (
	"context"

	core "github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/pkg/contracts"
)

// Without cgo there is no miniaudio; report the capability as missing rather
// than pretending to capture.
type source struct{}

func New() core.Source { return source{} }

func (source) Devices(context.Context) ([]core.Device, error) {
	return nil, contracts.Unsupported(contracts.CapMicrophone)
}

func (source) OpenRaw(context.Context, string) (core.RawStream, error) {
	return nil, contracts.Unsupported(contracts.CapMicrophone)
}
