//go:build linux

package main

import (
	"github.com/xlsft/openclick/internal/restraint"
	rlinux "github.com/xlsft/openclick/internal/restraint/linux"
)

func platformSignals() []restraint.Signal { return rlinux.Signals(rlinux.Options{}) }
