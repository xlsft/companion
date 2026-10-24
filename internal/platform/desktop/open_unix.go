//go:build linux || freebsd || openbsd || netbsd || dragonfly

package desktop

func opener(link string) (string, []string, error) { return "xdg-open", []string{link}, nil }
