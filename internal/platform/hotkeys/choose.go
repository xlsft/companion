package hotkeys

import (
	"context"
	"errors"
	"fmt"

	"github.com/xlsft/companion/pkg/contracts"
)

// deps are the backend constructors, replaced in tests.
type deps struct {
	x11    func() (backend, error)
	portal func(ctx context.Context) (backend, error)
	ipc    func() (backend, error)
}

// choose applies the selection table in the package doc. The control socket
// failing to listen (another instance, no XDG_RUNTIME_DIR) is fatal only
// when there is no primary backend either.
func choose(ctx context.Context, report contracts.CapabilityReport, d deps) (*Service, error) {
	ipc, ipcErr := d.ipc()
	if ipcErr != nil {
		ipc = nil
	}
	var primary backend
	var name string
	var perr error
	switch report.Session {
	case contracts.SessionX11:
		primary, perr = d.x11()
		name = BackendX11
	case contracts.SessionWayland:
		if report.Facts["portal.GlobalShortcuts.version"] != "absent" {
			primary, perr = d.portal(ctx)
			name = BackendPortal
		}
	}
	if perr != nil {
		primary = nil
	}
	if primary == nil && ipc == nil {
		return nil, fmt.Errorf("hotkeys: no backend: %w", errors.Join(perr, ipcErr, contracts.Unsupported(contracts.CapGlobalHotkey)))
	}
	return newService(primary, name, ipc), nil
}
