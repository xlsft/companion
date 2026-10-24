package session

import (
	"context"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

const waitTimeout = time.Second

// call records one Steps invocation.
type call struct {
	op   string
	ref  contracts.SessionRef
	ctx  context.Context
	text string
	req  Request
	emit Emitter
}

// blockingSteps records calls and blocks until the generation ends, so the
// test drives results by posting events itself.
type blockingSteps struct{ calls chan call }

func (s *blockingSteps) record(c call) error {
	s.calls <- c
	<-c.ctx.Done()
	return c.ctx.Err()
}

func (s *blockingSteps) StartRecording(ctx context.Context, ref contracts.SessionRef, emit Emitter) error {
	return s.record(call{op: "start_recording", ref: ref, ctx: ctx, emit: emit})
}

func (s *blockingSteps) FinishRecording(ctx context.Context, ref contracts.SessionRef) (string, error) {
	return "", s.record(call{op: "finish_recording", ref: ref, ctx: ctx})
}

func (s *blockingSteps) Capture(ctx context.Context, ref contracts.SessionRef) ([]*contracts.Capture, error) {
	return nil, s.record(call{op: "capture", ref: ref, ctx: ctx})
}

func (s *blockingSteps) Reason(ctx context.Context, ref contracts.SessionRef, req Request, emit Emitter) (Reply, error) {
	return Reply{}, s.record(call{op: "reason", ref: ref, ctx: ctx, req: req, emit: emit})
}

func (s *blockingSteps) Speak(ctx context.Context, ref contracts.SessionRef, text string, emit Emitter) error {
	return s.record(call{op: "speak", ref: ref, ctx: ctx, text: text, emit: emit})
}

type harness struct {
	t     *testing.T
	m     *Manager
	steps *blockingSteps
	snaps <-chan Snapshot
	calls []call // received but not yet consumed by waitCall
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	steps := &blockingSteps{calls: make(chan call, 128)}
	return newHarnessWith(t, steps, cfg)
}

func newHarnessWith(t *testing.T, steps Steps, cfg Config) *harness {
	t.Helper()
	m := New(steps, cfg)
	t.Cleanup(func() {
		m.Close()
		checkNoLeaks(t)
	})
	snaps, _ := m.Subscribe()
	h := &harness{t: t, m: m, snaps: snaps}
	if bs, ok := steps.(*blockingSteps); ok {
		h.steps = bs
	}
	if s := h.next(); s.State != StateIdle {
		t.Fatalf("initial snapshot %v, want idle", s.State)
	}
	return h
}

// sync waits until the loop processed everything posted before.
func (h *harness) sync() {
	h.t.Helper()
	if !h.m.exec(func() {}) {
		h.t.Fatal("manager closed")
	}
}

func (h *harness) post(ev Event) {
	h.t.Helper()
	if err := h.m.Post(ev); err != nil {
		h.t.Fatalf("post %T: %v", ev, err)
	}
	h.sync()
}

// postResult stamps ev with the active generation.
func (h *harness) postResult(ev Event) {
	h.t.Helper()
	h.post(ev.withRef(h.m.Current().Ref))
}

func (h *harness) next() Snapshot {
	h.t.Helper()
	select {
	case s, ok := <-h.snaps:
		if !ok {
			h.t.Fatal("snapshot channel closed")
		}
		return s
	case <-time.After(waitTimeout):
		h.t.Fatal("timed out waiting for snapshot")
	}
	return Snapshot{}
}

func (h *harness) expect(states ...State) []Snapshot {
	h.t.Helper()
	got := make([]Snapshot, 0, len(states))
	for i, want := range states {
		s := h.next()
		got = append(got, s)
		if s.State != want {
			h.t.Fatalf("snapshot %d: state %v, want %v (sequence %v)", i, s.State, want, statesOf(got))
		}
	}
	return got
}

func (h *harness) expectNone() {
	h.t.Helper()
	h.sync()
	select {
	case s := <-h.snaps:
		h.t.Fatalf("unexpected snapshot %+v", s)
	case <-time.After(20 * time.Millisecond):
	}
}

// waitCall returns the next call of op, keeping others for later.
func (h *harness) waitCall(op string) call {
	h.t.Helper()
	for i, c := range h.calls {
		if c.op == op {
			h.calls = append(h.calls[:i], h.calls[i+1:]...)
			return c
		}
	}
	deadline := time.After(waitTimeout)
	for {
		select {
		case c := <-h.steps.calls:
			if c.op == op {
				return c
			}
			h.calls = append(h.calls, c)
		case <-deadline:
			h.t.Fatalf("timed out waiting for step %q", op)
		}
	}
}

// allCalls collects exactly n calls and checks no more arrive.
func (h *harness) allCalls(n int) []string {
	h.t.Helper()
	var ops []string
	for _, c := range h.calls {
		ops = append(ops, c.op)
	}
	h.calls = nil
	deadline := time.After(waitTimeout)
	for len(ops) < n {
		select {
		case c := <-h.steps.calls:
			ops = append(ops, c.op)
		case <-deadline:
			h.t.Fatalf("got calls %v, want %d", ops, n)
		}
	}
	select {
	case c := <-h.steps.calls:
		h.t.Fatalf("unexpected extra call %q after %v", c.op, ops)
	case <-time.After(20 * time.Millisecond):
	}
	sort.Strings(ops)
	return ops
}

func statesOf(snaps []Snapshot) []State {
	out := make([]State, len(snaps))
	for i, s := range snaps {
		out[i] = s.State
	}
	return out
}

func waitDone(t *testing.T, ctx context.Context, within time.Duration) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(within):
		t.Fatalf("context not cancelled within %v", within)
	}
}

// checkNoLeaks fails if goroutines started by the package outlive Close.
func checkNoLeaks(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		var leaked []string
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		for _, g := range strings.Split(string(buf), "\n\n") {
			if strings.Contains(g, "internal/session.(*Manager)") ||
				strings.Contains(g, "internal/session.(*subscriber)") {
				leaked = append(leaked, g)
			}
		}
		if len(leaked) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%d leaked goroutines:\n%s", len(leaked), strings.Join(leaked, "\n\n"))
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
