//go:build !(linux && cgo)

package pipewire

import (
	"context"

	"github.com/xlsft/companion/pkg/contracts"
)

const Available = false

func GrabFrame(ctx context.Context, fd int, node uint32, opts Options) (*Frame, error) {
	return nil, contracts.Unsupported(contracts.CapScreenCapture)
}
