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

// InstalledFile is one file of an unpacked model directory.
type InstalledFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// InstalledModel is a model whose file was fully downloaded and verified,
// or whose archive was verified and unpacked into a directory.
type InstalledModel struct {
	ID string `json:"id"`
	// File is the model file, or for a directory model the directory name.
	File string `json:"file"`
	// Path is the file or directory the sidecar loads.
	Path string `json:"-"`
	// Size is the file size, or the sum of Files for a directory.
	Size int64 `json:"size"`
	// SHA256 is the model file's hash, or the archive's for a directory:
	// it identifies what was installed.
	SHA256     string    `json:"sha256"`
	VerifiedAt time.Time `json:"verified_at"`
	// Engine is the manifest's family; empty in records written before
	// sherpa models existed (all whisper).
	Engine string `json:"engine,omitempty"`
	// Dir marks an unpacked model; Files are its verified contents and
	// Archive the downloaded file name (to clean up a leftover .part).
	Dir     bool            `json:"dir,omitempty"`
	Files   []InstalledFile `json:"files,omitempty"`
	Archive string          `json:"archive,omitempty"`
}

// record is the <file>.ok sidecar. A model counts as installed only while the
// final file (or directory) and this record both exist, so a crash at any
// point of the download leaves the model uninstalled rather than half-usable.
type record = InstalledModel

func writeRecord(final string, m Manifest) error {
	return storeRecord(final, record{ID: m.ID, File: m.File, Size: m.Size, SHA256: m.SHA256, Engine: m.Family, VerifiedAt: time.Now().UTC()})
}

// storeRecord writes final.ok atomically.
func storeRecord(final string, rec record) error {
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

// present checks the files behind a record by size, as Installed does.
func present(rec record, path string) bool {
	st, err := os.Stat(path)
	if err != nil || !validSHA256(rec.SHA256) {
		return false
	}
	if !rec.Dir {
		return !st.IsDir() && st.Size() == rec.Size
	}
	if !st.IsDir() || len(rec.Files) == 0 {
		return false
	}
	for _, f := range rec.Files {
		if !bareName(f.Name) {
			return false
		}
		fst, err := os.Stat(filepath.Join(path, f.Name))
		if err != nil || fst.Size() != f.Size {
			return false
		}
	}
	return true
}

// Installed lists verified models in dir. It trusts the record and checks
// sizes instead of rehashing gigabytes on every call; .part files, unpack
// leftovers and files without a record are never listed.
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
		if !present(rec, path) {
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
		paths := []string{m.Path + okSuffix, m.Path, m.Path + partSuffix}
		if m.Dir {
			paths = []string{m.Path + okSuffix, m.Path, m.Path + unpackSuffix}
			if bareName(m.Archive) {
				paths = append(paths, filepath.Join(dir, m.Archive+partSuffix))
			}
		}
		var first error
		for _, p := range paths {
			if err := os.RemoveAll(p); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	return fmt.Errorf("%w: %s", ErrNotInstalled, id)
}
