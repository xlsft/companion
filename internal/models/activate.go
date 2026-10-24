package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// activeFile is the pointer to the model the sidecar should load. It lives
// next to the models so one directory holds the whole state.
const activeFile = "active.json"

var (
	// ErrNoActiveModel is returned when nothing is active, or the active
	// model's file is gone or no longer matches its verified record. The app
	// treats it as "go to setup": a deleted model cannot be used until a
	// replacement was confirmed.
	ErrNoActiveModel = errors.New("no active model")
	// ErrNoPrevious is returned by Rollback when there is nothing to go back to.
	ErrNoPrevious = errors.New("no previous model")
	// ErrChecksumMismatch blocks activation of a file that does not hash to
	// its record.
	ErrChecksumMismatch = errors.New("model checksum mismatch")
	// ErrSmokeFailed wraps the smoke tester's error.
	ErrSmokeFailed = errors.New("model smoke test failed")
	// ErrActiveModel is returned by Remove for the active model without force.
	ErrActiveModel = errors.New("model is active")
)

// ActiveModel is the persisted pointer.
type ActiveModel struct {
	ID          string    `json:"id"`
	Path        string    `json:"path"`
	SHA256      string    `json:"sha256"`
	ActivatedAt time.Time `json:"activated_at"`
	// PreviousID is the model that was active before; Rollback returns to it.
	PreviousID string `json:"previous_id,omitempty"`
}

// SmokeTester proves that a model actually loads and decodes. It must not
// judge the transcript: a tone is not speech.
type SmokeTester interface {
	Smoke(ctx context.Context, m InstalledModel) error
}

// SmokeFunc adapts a function to SmokeTester.
type SmokeFunc func(ctx context.Context, m InstalledModel) error

func (f SmokeFunc) Smoke(ctx context.Context, m InstalledModel) error { return f(ctx, m) }

// Activator switches the active model safely (PRD §9.5): the new model is
// verified and smoke-tested first, and only then the pointer is replaced in
// one atomic step, so the old model stays usable until the new one proved
// itself.
type Activator struct {
	// Dir is the models directory (holds the files, .ok records, active.json).
	Dir   string
	Smoke SmokeTester
	// Now is overridable for tests.
	Now func() time.Time

	mu sync.Mutex
	// afterTempWrite is a test hook that simulates a crash between writing
	// the temp file and renaming it.
	afterTempWrite func() error
}

type ActivateOption func(*activateOpts)

type activateOpts struct{ verify bool }

// WithVerify forces a full re-hash even when the record looks fresh.
func WithVerify() ActivateOption { return func(o *activateOpts) { o.verify = true } }

func (a *Activator) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func (a *Activator) pointerPath() string { return filepath.Join(a.Dir, activeFile) }

// Active returns the pointer only if the model is still installed and its
// file still matches the verified record; otherwise ErrNoActiveModel. It
// compares sizes instead of rehashing, like Installed.
func (a *Activator) Active() (ActiveModel, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active()
}

func (a *Activator) active() (ActiveModel, error) {
	p, err := a.readPointer()
	if err != nil {
		return ActiveModel{}, ErrNoActiveModel
	}
	m, ok, err := a.find(p.ID)
	if err != nil || !ok || m.SHA256 != p.SHA256 {
		return ActiveModel{}, ErrNoActiveModel
	}
	p.Path = m.Path
	return p, nil
}

func (a *Activator) readPointer() (ActiveModel, error) {
	var p ActiveModel
	data, err := os.ReadFile(a.pointerPath())
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	if p.ID == "" || !validSHA256(p.SHA256) {
		return p, errors.New("invalid active pointer")
	}
	return p, nil
}

func (a *Activator) find(id string) (InstalledModel, bool, error) {
	ms, err := Installed(a.Dir)
	if err != nil {
		return InstalledModel{}, false, err
	}
	for _, m := range ms {
		if m.ID == id {
			return m, true, nil
		}
	}
	return InstalledModel{}, false, nil
}

// Activate verifies, smoke-tests and then switches to the installed model
// id. Any failure leaves the current pointer untouched.
func (a *Activator) Activate(ctx context.Context, id string, opts ...ActivateOption) (ActiveModel, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var o activateOpts
	for _, f := range opts {
		f(&o)
	}
	m, ok, err := a.find(id)
	if err != nil {
		return ActiveModel{}, err
	}
	if !ok {
		return ActiveModel{}, fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	if err := a.verify(ctx, m, o.verify); err != nil {
		return ActiveModel{}, err
	}
	if a.Smoke == nil {
		return ActiveModel{}, fmt.Errorf("%w: no smoke tester configured", ErrSmokeFailed)
	}
	if err := a.Smoke.Smoke(ctx, m); err != nil {
		if ctx.Err() != nil {
			return ActiveModel{}, ctx.Err()
		}
		return ActiveModel{}, fmt.Errorf("%w: %w", ErrSmokeFailed, err)
	}

	next := ActiveModel{ID: m.ID, Path: m.Path, SHA256: m.SHA256, ActivatedAt: a.now()}
	if cur, err := a.active(); err == nil {
		if cur.ID == m.ID {
			next.PreviousID = cur.PreviousID // re-activation must not make a model its own fallback
		} else {
			next.PreviousID = cur.ID
		}
	}
	if err := a.writePointer(next); err != nil {
		return ActiveModel{}, err
	}
	return next, nil
}

// Rollback re-activates the previous model, smoke test included. The model
// being left becomes the new previous, so a second Rollback undoes it.
func (a *Activator) Rollback(ctx context.Context) (ActiveModel, error) {
	a.mu.Lock()
	p, err := a.readPointer()
	a.mu.Unlock()
	if err != nil || p.PreviousID == "" {
		return ActiveModel{}, ErrNoPrevious
	}
	return a.Activate(ctx, p.PreviousID)
}

// Remove deletes a model. The active one is refused unless force, in which
// case the pointer is cleared first (the app then returns to setup).
func (a *Activator) Remove(id string, force bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, err := a.readPointer(); err == nil && p.ID == id {
		if !force {
			return fmt.Errorf("%w: %s", ErrActiveModel, id)
		}
		if err := os.Remove(a.pointerPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		syncDir(a.Dir)
	}
	return Remove(a.Dir, id)
}

// verify re-hashes the file when asked, or when it changed after the record
// was written (a replaced file must never ride on an old verdict).
func (a *Activator) verify(ctx context.Context, m InstalledModel, force bool) error {
	st, err := os.Stat(m.Path)
	if err != nil {
		return err
	}
	if !force && !st.ModTime().After(m.VerifiedAt) {
		return nil
	}
	sum, err := hashFile(ctx, m.Path)
	if err != nil {
		return err
	}
	if sum != m.SHA256 {
		return fmt.Errorf("%w: %s", ErrChecksumMismatch, m.ID)
	}
	// Refresh the record so the next activation skips the hash.
	return writeRecord(m.Path, Manifest{ID: m.ID, File: m.File, Size: m.Size, SHA256: m.SHA256})
}

func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writePointer replaces active.json atomically: a crash leaves either the
// old or the new complete file, never a torn one.
func (a *Activator) writePointer(p ActiveModel) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	final := a.pointerPath()
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if a.afterTempWrite != nil {
		if err := a.afterTempWrite(); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	syncDir(a.Dir)
	return nil
}
