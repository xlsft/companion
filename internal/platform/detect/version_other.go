//go:build !linux && !windows && !darwin

package detect

func osVersion() string { return "" }
