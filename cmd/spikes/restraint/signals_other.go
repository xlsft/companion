//go:build !linux

package main

import (
	"github.com/xlsft/companion/internal/restraint"
	"github.com/xlsft/companion/internal/restraint/stub"
)

func platformSignals() []restraint.Signal { return stub.Signals() }
