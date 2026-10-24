package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func install(t *testing.T, dir, id, content string) InstalledModel {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	m := Manifest{ID: id, File: id + ".bin", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
	path := filepath.Join(dir, m.File)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(path, m); err != nil {
		t.Fatal(err)
	}
	return InstalledModel{ID: id, File: m.File, Path: path, Size: m.Size, SHA256: m.SHA256}
}

func okSmoke(context.Context, InstalledModel) error { return nil }

func newActivator(t *testing.T, smoke SmokeFunc) (*Activator, string) {
	dir := t.TempDir()
	return &Activator{Dir: dir, Smoke: smoke}, dir
}

func mustActivate(t *testing.T, a *Activator, id string) ActiveModel {
	t.Helper()
	p, err := a.Activate(context.Background(), id)
	if err != nil {
		t.Fatalf("activate %s: %v", id, err)
	}
	return p
}

func assertActive(t *testing.T, a *Activator, id, prev string) {
	t.Helper()
	p, err := a.Active()
	if err != nil || p.ID != id || p.PreviousID != prev {
		t.Fatalf("active = %+v err=%v, want %s (prev %q)", p, err, id, prev)
	}
}

func TestActivateAndSwitch(t *testing.T) {
	a, dir := newActivator(t, okSmoke)
	install(t, dir, "a", "model-a")
	install(t, dir, "b", "model-b")
	if _, err := a.Active(); !errors.Is(err, ErrNoActiveModel) {
		t.Fatalf("fresh: %v", err)
	}
	p := mustActivate(t, a, "a")
	if p.SHA256 == "" || p.ActivatedAt.IsZero() || p.Path != filepath.Join(dir, "a.bin") {
		t.Fatalf("pointer %+v", p)
	}
	assertActive(t, a, "a", "")
	mustActivate(t, a, "b")
	assertActive(t, a, "b", "a")
	mustActivate(t, a, "b") // re-activation keeps the real previous
	assertActive(t, a, "b", "a")
	if _, err := a.Activate(context.Background(), "nope"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestSmokeFailureKeepsPrevious(t *testing.T) {
	boom := errors.New("boom")
	a, dir := newActivator(t, func(_ context.Context, m InstalledModel) error {
		if m.ID == "bad" {
			return boom
		}
		return nil
	})
	install(t, dir, "good", "good")
	install(t, dir, "bad", "bad")
	mustActivate(t, a, "good")
	_, err := a.Activate(context.Background(), "bad")
	if !errors.Is(err, ErrSmokeFailed) || !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	assertActive(t, a, "good", "")
}

func TestChecksumMismatchBlocksActivation(t *testing.T) {
	smoked := false
	a, dir := newActivator(t, func(context.Context, InstalledModel) error { smoked = true; return nil })
	m := install(t, dir, "a", "model-a")
	good := install(t, dir, "g", "model-g")
	mustActivate(t, a, "g")
	smoked = false

	// Same size, different bytes, newer mtime than the record: re-hash.
	if err := os.WriteFile(m.Path, []byte("model-X"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(m.Path, future, future); err != nil {
		t.Fatal(err)
	}
	_, err := a.Activate(context.Background(), "a")
	if !errors.Is(err, ErrChecksumMismatch) || smoked {
		t.Fatalf("err=%v smoked=%v", err, smoked)
	}
	assertActive(t, a, "g", "")

	// On-demand verification catches corruption with an old mtime.
	if err := os.WriteFile(good.Path, []byte("model-Y"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(good.Path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Activate(context.Background(), "g"); err != nil {
		t.Fatalf("fresh-looking record should skip rehash: %v", err)
	}
	if _, err := a.Activate(context.Background(), "g", WithVerify()); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("WithVerify: %v", err)
	}
}

func TestRehashRefreshesRecord(t *testing.T) {
	a, dir := newActivator(t, okSmoke)
	m := install(t, dir, "a", "model-a")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(m.Path, future, future); err != nil {
		t.Fatal(err)
	}
	mustActivate(t, a, "a") // hashes OK, rewrites the record
	ms, _ := Installed(dir)
	if len(ms) != 1 || ms[0].VerifiedAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("record not refreshed: %+v", ms)
	}
}

func TestRollback(t *testing.T) {
	var fail bool
	a, dir := newActivator(t, func(context.Context, InstalledModel) error {
		if fail {
			return errors.New("smoke")
		}
		return nil
	})
	install(t, dir, "a", "model-a")
	install(t, dir, "b", "model-b")
	if _, err := a.Rollback(context.Background()); !errors.Is(err, ErrNoPrevious) {
		t.Fatalf("no pointer: %v", err)
	}
	mustActivate(t, a, "a")
	if _, err := a.Rollback(context.Background()); !errors.Is(err, ErrNoPrevious) {
		t.Fatalf("no previous: %v", err)
	}
	mustActivate(t, a, "b")

	fail = true
	if _, err := a.Rollback(context.Background()); !errors.Is(err, ErrSmokeFailed) {
		t.Fatalf("rollback smoke: %v", err)
	}
	assertActive(t, a, "b", "a")

	fail = false
	if _, err := a.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertActive(t, a, "a", "b")
	if err := Remove(dir, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Rollback(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("previous removed: %v", err)
	}
	assertActive(t, a, "a", "b")
}

func TestRemovedOrChangedFileMeansNoActiveModel(t *testing.T) {
	a, dir := newActivator(t, okSmoke)
	m := install(t, dir, "a", "model-a")
	mustActivate(t, a, "a")

	if err := os.Remove(m.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Active(); !errors.Is(err, ErrNoActiveModel) {
		t.Fatalf("removed file: %v", err)
	}

	// Reinstalled with different content: the pointer's hash no longer
	// matches the record.
	install(t, dir, "a", "model-Z-longer")
	if _, err := a.Active(); !errors.Is(err, ErrNoActiveModel) {
		t.Fatalf("replaced model: %v", err)
	}
	// A corrupt pointer is not a panic either.
	if err := os.WriteFile(filepath.Join(dir, activeFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Active(); !errors.Is(err, ErrNoActiveModel) {
		t.Fatalf("corrupt pointer: %v", err)
	}
}

func TestRemoveActive(t *testing.T) {
	a, dir := newActivator(t, okSmoke)
	install(t, dir, "a", "model-a")
	install(t, dir, "b", "model-b")
	mustActivate(t, a, "a")
	mustActivate(t, a, "b")

	if err := a.Remove("b", false); !errors.Is(err, ErrActiveModel) {
		t.Fatalf("remove active: %v", err)
	}
	assertActive(t, a, "b", "a")
	if err := a.Remove("a", false); err != nil { // inactive is fine
		t.Fatal(err)
	}
	if err := a.Remove("b", true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Active(); !errors.Is(err, ErrNoActiveModel) {
		t.Fatalf("after force remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, activeFile)); !os.IsNotExist(err) {
		t.Fatalf("pointer not cleared: %v", err)
	}
	assertInstalled(t, dir, 0)
}

func TestCrashBeforeRenameKeepsOldPointer(t *testing.T) {
	a, dir := newActivator(t, okSmoke)
	install(t, dir, "a", "model-a")
	install(t, dir, "b", "model-b")
	mustActivate(t, a, "a")

	a.afterTempWrite = func() error { return errors.New("simulated crash") }
	if _, err := a.Activate(context.Background(), "b"); err == nil {
		t.Fatal("expected failure")
	}
	// A fresh Activator (new process) sees the old pointer, stray temp
	// file and all.
	if _, err := os.Stat(filepath.Join(dir, activeFile+".tmp")); err != nil {
		t.Fatalf("temp should remain: %v", err)
	}
	a2 := &Activator{Dir: dir, Smoke: SmokeFunc(okSmoke)}
	assertActive(t, a2, "a", "")
	mustActivate(t, a2, "b") // and the leftover temp does not block the next switch
	assertActive(t, a2, "b", "a")
}

func TestActivateHonoursCancellation(t *testing.T) {
	a, dir := newActivator(t, func(ctx context.Context, _ InstalledModel) error {
		<-ctx.Done()
		return ctx.Err()
	})
	install(t, dir, "a", "model-a")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := a.Activate(ctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if _, err := a.Active(); !errors.Is(err, ErrNoActiveModel) {
		t.Fatal("pointer written on cancel")
	}
}

var (
	buildOnce sync.Once
	sidecar   string
)

func buildSidecar(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "models-sidecar")
		if err != nil {
			return
		}
		bin := filepath.Join(dir, "stt-sidecar")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		if exec.Command("go", "build", "-o", bin, "github.com/xlsft/companion/cmd/stt-sidecar").Run() == nil {
			sidecar = bin
		}
	})
	if sidecar == "" {
		t.Skip("could not build cmd/stt-sidecar")
	}
	return sidecar
}

func TestSidecarSmoke(t *testing.T) {
	bin := buildSidecar(t)
	dir := t.TempDir()
	m := install(t, dir, "a", "model-a")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	good := SidecarSmoke{Path: bin, Args: []string{"-engine", "fake"}}
	if err := good.Smoke(ctx, m); err != nil {
		t.Fatalf("good sidecar: %v", err)
	}
	// A sidecar that dies during the final decode must fail the test.
	crash := SidecarSmoke{Path: bin, Args: []string{"-engine", "fake", "-fake-exit-on-final"}}
	if err := crash.Smoke(ctx, m); err == nil {
		t.Fatal("crashing sidecar passed")
	}
	// A sidecar that cannot start fails quickly instead of hanging.
	missing := SidecarSmoke{Path: filepath.Join(dir, "missing")}
	if err := missing.Smoke(ctx, m); err == nil {
		t.Fatal("missing sidecar passed")
	}

	// End to end through the Activator.
	a := &Activator{Dir: dir, Smoke: good}
	mustActivate(t, a, "a")
	assertActive(t, a, "a", "")
}

func TestSmokeSignal(t *testing.T) {
	s := SmokeSignal()
	if len(s) != 32000 {
		t.Fatalf("len %d", len(s))
	}
	var head, tail int
	for i, v := range s {
		if v != 0 {
			if i < 16000 {
				head++
			} else {
				tail++
			}
		}
	}
	if head != 0 || tail < 15000 {
		t.Fatalf("head=%d tail=%d", head, tail)
	}
}
