package wizard

import (
	"github.com/xlsft/companion/internal/readiness"
)

// stageState is one end-to-end stage as its card shows it.
type stageState int

const (
	stageWaiting stageState = iota // no run yet
	stageRunning
	stageDone
	stageFailed
	stageNotRun // a run ended before this stage
)

func stageOf(s snapshot, stage string) (stageState, string) {
	if s.running {
		return stageRunning, ""
	}
	r, ok := s.result(readiness.StepEndToEnd)
	if !ok {
		return stageWaiting, ""
	}
	switch v := r.Detail[stage]; v {
	case readiness.DetailOK, "passed":
		return stageDone, ""
	case "", readiness.DetailSkipped, "pending":
		return stageNotRun, ""
	default:
		return stageFailed, v
	}
}
