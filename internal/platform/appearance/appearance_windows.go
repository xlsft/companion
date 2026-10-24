//go:build windows

package appearance

import (
	"context"

	"golang.org/x/sys/windows/registry"

	"github.com/xlsft/companion/pkg/contracts"
)

// New returns a source over HKCU\...\Themes\Personalize AppsUseLightTheme.
// It polls: the value is one DWORD read, and a registry change event would
// need a handle and goroutine for no visible gain.
func New() contracts.AppearanceSource { return &winSource{state: newState()} }

type winSource struct{ *state }

func (w *winSource) Start(ctx context.Context) error {
	dark, err := readAppsUseLightTheme()
	if err != nil {
		return contracts.Unsupported(contracts.CapColorScheme)
	}
	w.set(dark)
	go w.poll(ctx, pollEvery, func(context.Context) (bool, error) { return readAppsUseLightTheme() })
	return nil
}

func readAppsUseLightTheme() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false, err
	}
	return v == 0, nil
}
