//go:build !(linux || freebsd || openbsd || netbsd || dragonfly || darwin || windows)

package desktop

import "github.com/xlsft/companion/pkg/contracts"

func opener(string) (string, []string, error) { return "", nil, contracts.Unsupported(CapOpenURL) }
