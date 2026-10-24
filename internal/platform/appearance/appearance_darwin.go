//go:build darwin

package appearance

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// New returns a source that polls `defaults read -g AppleInterfaceStyle`
// (no cgo): it prints "Dark" in dark mode and fails when the key is unset,
// which is light mode. Not verified on a Mac.
func New() contracts.AppearanceSource { return &macSource{state: newState()} }

type macSource struct{ *state }

func (m *macSource) Start(ctx context.Context) error {
	if _, err := exec.LookPath("defaults"); err != nil {
		return contracts.Unsupported(contracts.CapColorScheme)
	}
	v, _ := readInterfaceStyle(ctx)
	m.set(v)
	go m.poll(ctx, pollEvery, readInterfaceStyle)
	return nil
}

func readInterfaceStyle(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleInterfaceStyle").Output()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil // key absent: light
	}
	return strings.EqualFold(strings.TrimSpace(string(out)), "dark"), nil
}
