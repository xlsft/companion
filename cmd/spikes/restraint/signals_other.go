//go:build !linux

package main

import (
	"github.com/xlsft/openclick/internal/restraint"
	"github.com/xlsft/openclick/internal/restraint/stub"
)

func platformSignals() []restraint.Signal { return stub.Signals() }
