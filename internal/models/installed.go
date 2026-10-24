package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotInstalled is returned by Remove for an unknown model id.
var ErrNotInstalled = errors.New("model not installed")

// InstalledModel is a model whose file was fully downloaded and verified.
type InstalledModel struct {
	ID         string    `json:"id"`
	File       string    `json:"file"`
	Path       string    `json:"-"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	VerifiedAt time.Time `json:"verified_at"`
}

// record is the <file>.ok sidecar. A model counts as installed only while the
// final file and this record both exist, so a crash at any point of the
// download leaves the model uninstalled rather than half-usable.
type record = InstalledModel

func writeRecord(final string, m Manifest) error {
	rec := record{ID: m.ID, File: m.File, Size: m.Size, SHA256: m.SHA256, VerifiedAt: time.Now().UTC()}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	ok := final + okSuffix
	tmp := ok + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, ok); err != nil {
		return err
	}
	syncDir(filepath.Dir(final))
	return nil
}

func readRecord(path string) (record, error) {
	var rec record
	data, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, err
	}
	return rec, nil
}

// Installed lists verified models in dir. It trusts the record and checks the
// file's size instead of rehashing gigabytes on every call; .part files and
// files without a record are never listed.
func Installed(dir string) ([]InstalledModel, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []InstalledModel
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), okSuffix) {
			continue
		}
		rec, err := readRecord(filepath.Join(dir, e.Name()))
		if err != nil || !bareName(rec.File) || rec.File+okSuffix != e.Name() {
			continue
		}
		path := filepath.Join(dir, rec.File)
		st, err := os.Stat(path)
		if err != nil || st.Size() != rec.Size || !validSHA256(rec.SHA256) {
			continue
		}
		rec.Path = path
		out = append(out, rec)
	}
	return out, nil
}

// Remove deletes an installed model by id, including leftovers. The record
// goes first so an interrupted removal never leaves a "installed" stub.
func Remove(dir, id string) error {
	ms, err := Installed(dir)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.ID != id {
			continue
		}
		var first error
		for _, p := range []string{m.Path + okSuffix, m.Path, m.Path + partSuffix} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) && first == nil {
				first = err
			}
		}
		return first
	}
	return fmt.Errorf("%w: %s", ErrNotInstalled, id)
}
