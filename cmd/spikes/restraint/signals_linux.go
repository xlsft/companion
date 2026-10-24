//go:build linux

package main

import (
	"github.com/xlsft/companion/internal/restraint"
	rlinux "github.com/xlsft/companion/internal/restraint/linux"
)

func platformSignals() []restraint.Signal { return rlinux.Signals(rlinux.Options{}) }
