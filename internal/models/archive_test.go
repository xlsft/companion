package models

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/stt/engine"
)

// testdata/tiny-model.tar.bz2 mimics a sherpa-onnx release archive: the
// int8 model, a full-precision copy and test audio that must be skipped,
// and tokens.txt.
func archiveFixture(t *testing.T) (Manifest, *Downloader, *atomic.Int64) {
	t.Helper()
	data, err := os.ReadFile("testdata/tiny-model.tar.bz2")
	if err != nil {
		t.Fatal(err)
	}
	var served atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		http.ServeContent(w, r, "a.tar.bz2", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(data)
	m := Manifest{ID: "tiny", Name: "Tiny", Family: FamilySherpa, File: "tiny-model.tar.bz2", URL: srv.URL + "/a.tar.bz2",
		Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Languages: []string{"en"},
		Format: FormatTarBz2, Kind: KindOffline, ModelType: "sense_voice",
		Files: []ArchiveFile{
			{Role: "model", Path: "tiny-model/model.int8.onnx", Size: 22},
			{Role: "tokens", Path: "tiny-model/tokens.txt", Size: 12},
		}}
	d := &Downloader{Client: srv.Client(), FreeSpace: func(string) (uint64, error) { return 1 << 40, nil }}
	return m, d, &served
}

func TestArchiveInstall(t *testing.T) {
	m, d, served := archiveFixture(t)
	dir := t.TempDir()
	var sawUnpack bool
	path, err := d.Download(context.Background(), m, dir, func(p Progress) { sawUnpack = sawUnpack || p.Unpacking })
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "tiny") || !sawUnpack {
		t.Fatalf("path %s, unpack progress %v", path, sawUnpack)
	}
	assertFile(t, filepath.Join(path, "model.int8.onnx"), []byte("model-bytes-0123456789"))
	for _, skipped := range []string{"model.onnx", "test_wavs", "0.wav"} {
		if _, err := os.Stat(filepath.Join(path, skipped)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be unpacked", skipped)
		}
	}
	for _, gone := range []string{m.File + partSuffix, "tiny" + unpackSuffix} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", gone)
		}
	}
	desc, err := engine.ReadDescriptor(path)
	if err != nil || desc.Type != "sense_voice" || desc.Files["model"] != "model.int8.onnx" || desc.Engine != FamilySherpa {
		t.Fatalf("descriptor %+v, %v", desc, err)
	}
	ms, err := Installed(dir)
	if err != nil || len(ms) != 1 || !ms[0].Dir || ms[0].Path != path || ms[0].Size != 34 || ms[0].SHA256 != m.SHA256 || ms[0].Engine != FamilySherpa {
		t.Fatalf("installed %+v, %v", ms, err)
	}
	// Installed again: no network.
	n := served.Load()
	if _, err := d.Download(context.Background(), m, dir, nil); err != nil || served.Load() != n {
		t.Fatalf("reinstall: %v, requests %d -> %d", err, n, served.Load())
	}
	// A changed file drops the model from Installed.
	if err := os.WriteFile(filepath.Join(path, "tokens.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, dir, 0)
}

// A complete archive left as .part (download finished, unpack interrupted)
// is hashed and unpacked without fetching again; a corrupt one is deleted.
func TestArchiveResume(t *testing.T) {
	data, err := os.ReadFile("testdata/tiny-model.tar.bz2")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		part     []byte
		wantErr  error
		requests int64
	}{
		{"complete part", data, nil, 0},
		{"half part", data[:len(data)/2], nil, 1},
		{"corrupt part", append(bytes.Repeat([]byte{0}, len(data)-1), 1), ErrChecksum, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, d, served := archiveFixture(t)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, m.File+partSuffix), tt.part, 0o644); err != nil {
				t.Fatal(err)
			}
			// A stale unpack directory from a crash must not leak into the model.
			stale := filepath.Join(dir, "tiny"+unpackSuffix)
			if err := os.MkdirAll(stale, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stale, "junk"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := d.Download(context.Background(), m, dir, nil)
			if !errors.Is(err, tt.wantErr) && !(err == nil && tt.wantErr == nil) {
				t.Fatalf("err %v, want %v", err, tt.wantErr)
			}
			if served.Load() != tt.requests {
				t.Fatalf("requests %d, want %d", served.Load(), tt.requests)
			}
			if tt.wantErr != nil {
				if _, err := os.Stat(filepath.Join(dir, m.File+partSuffix)); !os.IsNotExist(err) {
					t.Fatal("corrupt part kept")
				}
				return
			}
			if _, err := os.Stat(filepath.Join(dir, "tiny", "junk")); !os.IsNotExist(err) {
				t.Fatal("stale unpack content installed")
			}
			assertInstalled(t, dir, 1)
		})
	}
}

func TestArchiveMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"missing member", func(m *Manifest) { m.Files[0].Path = "tiny-model/encoder.onnx" }},
		{"wrong size", func(m *Manifest) { m.Files[1].Size = 13 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, d, _ := archiveFixture(t)
			tt.mutate(&m)
			dir := t.TempDir()
			if _, err := d.Download(context.Background(), m, dir, nil); !errors.Is(err, ErrArchive) {
				t.Fatalf("got %v", err)
			}
			assertInstalled(t, dir, 0)
			if _, err := os.Stat(filepath.Join(dir, "tiny"+unpackSuffix)); !os.IsNotExist(err) {
				t.Fatal("unpack dir left behind")
			}
		})
	}
}

func TestArchiveSpaceCheck(t *testing.T) {
	m, d, _ := archiveFixture(t)
	// Room for the archive alone is not enough: it and the files coexist.
	d.FreeSpace = func(string) (uint64, error) { return uint64(m.Size) + 5, nil }
	if _, err := d.Download(context.Background(), m, t.TempDir(), nil); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("got %v", err)
	}
}

func TestArchiveActivateAndRemove(t *testing.T) {
	m, d, _ := archiveFixture(t)
	dir := t.TempDir()
	if _, err := d.Download(context.Background(), m, dir, nil); err != nil {
		t.Fatal(err)
	}
	var smoked string
	a := &Activator{Dir: dir, Smoke: SmokeFunc(func(_ context.Context, im InstalledModel) error { smoked = im.Path; return nil })}
	if _, err := a.Activate(context.Background(), "tiny", WithVerify()); err != nil {
		t.Fatal(err)
	}
	if smoked != filepath.Join(dir, "tiny") {
		t.Fatalf("smoke saw %q", smoked)
	}
	// Same size, different bytes: only a hash catches it.
	if err := os.WriteFile(filepath.Join(dir, "tiny", "model.int8.onnx"), []byte("model-bytes-XXXXXXXXXX"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Activate(context.Background(), "tiny", WithVerify()); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("tampered: %v", err)
	}
	if err := a.Remove("tiny", true); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestArchiveManifestValidate(t *testing.T) {
	m, _, _ := archiveFixture(t)
	tests := []struct {
		name   string
		mutate func(*Manifest)
		ok     bool
	}{
		{"good", func(*Manifest) {}, true},
		{"no files", func(m *Manifest) { m.Files = nil }, false},
		{"no tokens", func(m *Manifest) { m.Files = m.Files[:1] }, false},
		{"no model type", func(m *Manifest) { m.ModelType = "" }, false},
		{"escape", func(m *Manifest) { m.Files[0].Path = "../model.int8.onnx" }, false},
		{"absolute", func(m *Manifest) { m.Files[0].Path = "/etc/passwd" }, false},
		{"dup base name", func(m *Manifest) { m.Files[1].Path = "other/model.int8.onnx" }, false},
		{"dup role", func(m *Manifest) { m.Files[1].Role = "model" }, false},
		{"bad kind", func(m *Manifest) { m.Kind = "batch" }, false},
		{"bad format", func(m *Manifest) { m.Format = "zip" }, false},
		{"files without archive", func(m *Manifest) { m.Format = "" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mm := m
			mm.Files = append([]ArchiveFile(nil), m.Files...)
			tt.mutate(&mm)
			if err := mm.Validate(); (err == nil) != tt.ok {
				t.Fatalf("err=%v want ok=%v", err, tt.ok)
			}
		})
	}
}
