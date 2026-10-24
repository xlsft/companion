//go:build linux

package insert

import "github.com/xlsft/companion/pkg/contracts"

// Options configure New.
type Options struct {
	// TokenPath keeps the RemoteDesktop portal's restore token, so its
	// consent dialog is shown once; "" asks once per run.
	TokenPath string
	// UseClipboard is the user's clipboard-fallback opt-in.
	UseClipboard func() bool
}

// New builds the inserter for the running session. XTest reaches only X
// clients, so a Wayland session never uses it, even with Xwayland.
func New(report contracts.CapabilityReport, o Options) *Inserter {
	in := &Inserter{UseClipboard: o.UseClipboard}
	switch report.Session {
	case contracts.SessionWayland:
		in.Typers = []Typer{newPortalTyper(o.TokenPath), newVirtualKeyboard()}
		in.Clipboard = newCmdClipboard(true)
	case contracts.SessionX11:
		in.Typers = []Typer{newXTest()}
		in.Clipboard = newCmdClipboard(false)
	}
	return in
}
