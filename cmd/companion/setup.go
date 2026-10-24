package main

import (
	"context"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/internal/ui/wizard"
)

// openGate loads the persisted setup progress from the settings KV.
func openGate(ctx context.Context, kv *store.KV, checks map[readiness.Step]readiness.Check) (*readiness.Gate, error) {
	return readiness.Open(ctx, readiness.KVStore{KV: kv}, checks)
}

// setupChecks are the wizard's step checks. None is wired yet, so every
// step fails with not_wired and a real run stays in setup: the gate must
// never pass a check that did not run (FR-ONB-008).
func (c *core) setupChecks() map[readiness.Step]readiness.Check {
	checks := map[readiness.Step]readiness.Check{}
	for _, st := range readiness.Steps {
		checks[st] = func(context.Context) readiness.Result {
			return readiness.Result{Status: readiness.Failed, Code: "not_wired"}
		}
	}
	return checks
}

// setupBackends are the wizard's step backends; nil ones leave their
// controls disabled.
func (c *core) setupBackends() wizard.Backends { return wizard.Backends{} }
