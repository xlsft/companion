// Package desktop holds small OS integrations of the core process:
// opening a link in the user's browser and restarting into a new
// executable after an update.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"

	"github.com/xlsft/companion/pkg/contracts"
)

// Capabilities for contracts.Unsupported.
const (
	CapOpenURL contracts.Capability = "open_url"
	CapReexec  contracts.Capability = "reexec"
)

// ErrBadURL: only web links are handed to the system opener, never files
// or custom schemes a release feed could smuggle in.
var ErrBadURL = errors.New("desktop: not a web link")

// OpenURL opens an http(s) link with the system's default handler.
func OpenURL(ctx context.Context, link string) error {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ErrBadURL
	}
	name, args, err := opener(u.String())
	if err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, name, args...).Run(); err != nil {
		return fmt.Errorf("desktop: open link: %w", err)
	}
	return nil
}
