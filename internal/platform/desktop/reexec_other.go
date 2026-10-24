//go:build !(unix || windows)

package desktop

import "github.com/xlsft/companion/pkg/contracts"

// Reexec is unavailable here: the user restarts by hand.
func Reexec(string, []string) error { return contracts.Unsupported(CapReexec) }
