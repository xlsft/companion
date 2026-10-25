package readiness

import (
	"context"
	"errors"
	"testing"
)

type memStore struct {
	data    []byte
	failErr error
}

func (m *memStore) Load(context.Context) ([]byte, error) { return m.data, nil }
func (m *memStore) Save(_ context.Context, d []byte) error {
	if m.failErr != nil {
		return m.failErr
	}
	m.data = append([]byte(nil), d...)
	return nil
}

func pass(context.Context) Result { return Result{Status: Passed} }
func fail(code string) Check {
	return func(context.Context) Result { return Result{Status: Failed, Code: code} }
}

func allChecks(c Check) map[Step]Check {
	m := map[Step]Check{}
	for _, s := range Steps {
		m[s] = c
	}
	return m
}

func runAll(t *testing.T, g *Gate) {
	t.Helper()
	for _, s := range Steps {
		if _, err := g.Run(context.Background(), s); err != nil {
			t.Fatalf("run %s: %v", s, err)
		}
	}
}

func TestFreshInstallIsSetupOnly(t *testing.T) {
	g, err := Open(context.Background(), &memStore{}, allChecks(pass))
	if err != nil {
		t.Fatal(err)
	}
	p := g.Progress()
	if p.State != SetupRequired || p.Current != StepPermissions {
		t.Fatalf("fresh progress %+v", p)
	}
	if !errors.Is(g.Require(), ErrNotReady) {
		t.Fatal("Require must refuse before Ready")
	}
	select {
	case <-g.ReadyCh():
		t.Fatal("ready channel closed before setup")
	default:
	}
}

func TestOnlyEndToEndIsLocked(t *testing.T) {
	ctx := context.Background()
	others := []Step{StepPlatform, StepTTS, StepSTT, StepPermissions, StepBYOK} // any order
	tests := []struct {
		name    string
		run     []Step
		e2eLock bool
	}{
		{name: "fresh", e2eLock: true},
		{name: "voice before permissions", run: others[1:3], e2eLock: true},
		{name: "all but byok", run: others[:4], e2eLock: true},
		{name: "all, out of order", run: others},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, _ := Open(ctx, &memStore{}, allChecks(pass))
			for _, s := range tt.run {
				if _, err := g.Run(ctx, s); err != nil {
					t.Fatalf("run %s: %v", s, err)
				}
			}
			_, err := g.Run(ctx, StepEndToEnd)
			if locked := errors.Is(err, ErrStepLocked); locked != tt.e2eLock {
				t.Fatalf("end-to-end locked=%v err=%v", locked, err)
			}
			if g.IsReady() == tt.e2eLock {
				t.Fatalf("ready=%v", g.IsReady())
			}
		})
	}
}

func TestStepChangedDuringEndToEnd(t *testing.T) {
	ctx := context.Background()
	checks := allChecks(pass)
	var g *Gate
	checks[StepEndToEnd] = func(context.Context) Result {
		// The user switches provider while the full test runs.
		if _, err := g.Run(ctx, StepBYOK); err != nil {
			t.Error(err)
		}
		return Result{Status: Passed}
	}
	g, _ = Open(ctx, &memStore{}, checks)
	for _, s := range Steps[:len(Steps)-1] {
		_, _ = g.Run(ctx, s)
	}
	res, err := g.Run(ctx, StepEndToEnd)
	if err != nil || res.Status != Pending || g.IsReady() {
		t.Fatalf("res %+v err %v ready %v", res, err, g.IsReady())
	}
}

func TestValidatingWhileAnyCheckRuns(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	started := make(chan struct{})
	checks := allChecks(pass)
	checks[StepPermissions] = func(context.Context) Result {
		close(started)
		<-release
		return Result{Status: Passed}
	}
	g, _ := Open(ctx, &memStore{}, checks)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = g.Run(ctx, StepPermissions)
	}()
	<-started
	_, _ = g.Run(ctx, StepBYOK)
	if st := g.Progress().State; st != Validating {
		t.Fatalf("state %s while permissions still run", st)
	}
	close(release)
	<-done
	if p := g.Progress(); p.State != Configuring || p.Results[StepPermissions].Status != Passed {
		t.Fatalf("after both: %+v", p)
	}
}

func TestReadyOnlyAfterEndToEnd(t *testing.T) {
	st := &memStore{}
	g, _ := Open(context.Background(), st, allChecks(pass))
	for _, s := range Steps[:len(Steps)-1] {
		if _, err := g.Run(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if g.IsReady() {
			t.Fatalf("ready after %s", s)
		}
	}
	if _, err := g.Run(context.Background(), StepEndToEnd); err != nil {
		t.Fatal(err)
	}
	if !g.IsReady() || g.Require() != nil {
		t.Fatal("not ready after all checks passed")
	}
	select {
	case <-g.ReadyCh():
	default:
		t.Fatal("ready channel not closed")
	}

	// A normal restart stays Ready without re-running setup.
	g2, _ := Open(context.Background(), st, allChecks(fail("x")))
	if !g2.IsReady() {
		t.Fatal("Ready lost across restart")
	}
}

// After Ready a step can be checked again (a voice changed in Settings):
// the new result is kept and shown, and Ready stays until a restart.
func TestRecheckAfterReady(t *testing.T) {
	tests := []struct {
		name   string
		check  Check
		status CheckStatus
		code   string
	}{
		{"passes", pass, Passed, ""},
		{"fails", fail("playback_failed"), Failed, "playback_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &memStore{}
			checks := allChecks(pass)
			g, _ := Open(context.Background(), st, checks)
			runAll(t, g)
			if !g.IsReady() {
				t.Fatal("not ready")
			}
			checks[StepTTS] = tt.check
			res, err := g.Run(context.Background(), StepTTS)
			if err != nil || res.Status != tt.status || res.Code != tt.code {
				t.Fatalf("res %+v err %v", res, err)
			}
			if r := g.Progress().Results[StepTTS]; r.Status != tt.status || r.Code != tt.code {
				t.Fatalf("stored %+v", r)
			}
			if !g.IsReady() {
				t.Fatal("a re-check left Ready")
			}
			// A restart opens setup on what is broken.
			g2, _ := Open(context.Background(), st, checks)
			if g2.IsReady() != (tt.status == Passed) || g2.Progress().Results[StepTTS].Code != tt.code {
				t.Fatalf("after restart: ready %v %+v", g2.IsReady(), g2.Progress().Results[StepTTS])
			}
		})
	}
}

func TestFailureStaysOnStep(t *testing.T) {
	checks := allChecks(pass)
	checks[StepTTS] = fail("tts.auth")
	st := &memStore{}
	g, _ := Open(context.Background(), st, checks)
	for _, s := range []Step{StepPermissions, StepBYOK, StepSTT, StepPlatform} {
		_, _ = g.Run(context.Background(), s)
	}
	res, err := g.Run(context.Background(), StepTTS)
	if err != nil || res.Status != Failed || res.Code != "tts.auth" {
		t.Fatalf("res %+v err %v", res, err)
	}
	if _, err := g.Run(context.Background(), StepEndToEnd); !errors.Is(err, ErrStepLocked) {
		t.Fatalf("end-to-end with a failed step: %v", err)
	}
	if p := g.Progress(); p.Current != StepTTS || p.State != Configuring {
		t.Fatalf("progress %+v", p)
	}
	// Restart resumes at the failing step (FR-ONB-011).
	g2, _ := Open(context.Background(), st, checks)
	if p := g2.Progress(); p.Current != StepTTS {
		t.Fatalf("resumed at %s", p.Current)
	}
}

func TestRerunInvalidatesEndToEnd(t *testing.T) {
	g, _ := Open(context.Background(), &memStore{}, allChecks(pass))
	for _, s := range Steps[:len(Steps)-1] {
		_, _ = g.Run(context.Background(), s)
	}
	_, _ = g.Run(context.Background(), StepBYOK) // user changed provider
	if p := g.Progress(); p.Results[StepEndToEnd].Status == Passed {
		t.Fatal("end-to-end kept after an earlier step re-ran")
	}
}

func TestSaveFailureDoesNotReachReady(t *testing.T) {
	st := &memStore{}
	g, _ := Open(context.Background(), st, allChecks(pass))
	for _, s := range Steps[:len(Steps)-1] {
		_, _ = g.Run(context.Background(), s)
	}
	st.failErr = errors.New("disk full")
	if _, err := g.Run(context.Background(), StepEndToEnd); err == nil {
		t.Fatal("expected save error")
	}
	if g.IsReady() {
		t.Fatal("Ready without a durable record")
	}
}

func TestRevokeReturnsToSetup(t *testing.T) {
	st := &memStore{}
	g, _ := Open(context.Background(), st, allChecks(pass))
	runAll(t, g)
	if err := g.Revoke(context.Background(), StepSTT); err != nil {
		t.Fatal(err)
	}
	if g.IsReady() || g.Progress().Current != StepSTT {
		t.Fatalf("after revoke: %+v", g.Progress())
	}
	g2, _ := Open(context.Background(), st, allChecks(pass))
	if g2.IsReady() {
		t.Fatal("revocation not persisted")
	}
}

func TestCorruptRecordDoesNotUnlock(t *testing.T) {
	g, err := Open(context.Background(), &memStore{data: []byte(`{"state":"ready"`)}, allChecks(pass))
	if err != nil {
		t.Fatal(err)
	}
	if g.IsReady() {
		t.Fatal("corrupt progress unlocked the app")
	}
	forged, _ := Open(context.Background(), &memStore{data: []byte(`{"state":"ready","results":{}}`)}, allChecks(pass))
	if forged.IsReady() {
		t.Fatal("ready without passed results")
	}
}

func TestCancelledCheckIsPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	checks := allChecks(pass)
	checks[StepPermissions] = func(ctx context.Context) Result {
		cancel()
		return Result{Status: Failed, Code: "cancelled"}
	}
	g, _ := Open(context.Background(), &memStore{}, checks)
	res, _ := g.Run(ctx, StepPermissions)
	if res.Status != Pending {
		t.Fatalf("cancelled check recorded as %s", res.Status)
	}
}

func TestSubscribe(t *testing.T) {
	g, _ := Open(context.Background(), &memStore{}, allChecks(pass))
	ch, unsub := g.Subscribe()
	defer unsub()
	if p := <-ch; p.State != SetupRequired {
		t.Fatalf("initial %+v", p)
	}
	_, _ = g.Run(context.Background(), StepPermissions)
	if p := <-ch; p.Results[StepPermissions].Status != Passed {
		t.Fatalf("after run %+v", p)
	}
}
