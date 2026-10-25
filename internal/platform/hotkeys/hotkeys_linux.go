package hotkeys

import (
	"context"

	"github.com/xlsft/companion/internal/platform/control"
	waylandhotkeys "github.com/xlsft/companion/internal/platform/linuxwayland/hotkeys"
	x11hotkeys "github.com/xlsft/companion/internal/platform/linuxx11/hotkeys"
	"github.com/xlsft/companion/pkg/contracts"
)

// The backends that can see a double tap (OC-133).
var (
	_ contracts.DoubleTapper = (*x11hotkeys.Service)(nil)
	_ contracts.DoubleTapper = (*control.Server)(nil)
	_ contracts.DoubleTapper = (*Service)(nil)
)

// New starts the hotkey service for the session in report, with the
// control socket at control.DefaultPath. The caller must Close it.
func New(ctx context.Context, report contracts.CapabilityReport) (*Service, error) {
	return NewWithSocket(ctx, report, "")
}

// NewWithSocket is New with an explicit control socket path ("" = default).
func NewWithSocket(ctx context.Context, report contracts.CapabilityReport, socket string) (*Service, error) {
	return choose(ctx, report, deps{
		x11: func() (backend, error) { return x11hotkeys.Open() },
		portal: func(ctx context.Context) (backend, error) {
			return waylandhotkeys.Open(ctx)
		},
		ipc: func() (backend, error) {
			path := socket
			if path == "" {
				p, err := control.DefaultPath()
				if err != nil {
					return nil, err
				}
				path = p
			}
			return control.Listen(path)
		},
	})
}
