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
)

type fixture struct {
	data []byte
	m    Manifest
	srv  *httptest.Server
	d    *Downloader
}

func newFixture(t *testing.T, size int, h func(f *fixture) http.Handler) *fixture {
	t.Helper()
	f := &fixture{data: make([]byte, size)}
	for i := range f.data {
		f.data[i] = byte(i*7 + i>>8)
	}
	sum := sha256.Sum256(f.data)
	f.srv = httptest.NewTLSServer(h(f))
	t.Cleanup(f.srv.Close)
	f.m = Manifest{ID: "t", Name: "t", Family: "whisper", File: "t.bin", URL: f.srv.URL + "/t.bin", Size: int64(size),
		SHA256: hex.EncodeToString(sum[:]), Languages: []string{"multi"}}
	f.d = &Downloader{Client: f.srv.Client(), FreeSpace: func(string) (uint64, error) { return 1 << 40, nil }}
	return f
}

func serveRange(f *fixture) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "t.bin", time.Time{}, bytes.NewReader(f.data))
	})
}

func assertFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("file %s mismatch (err=%v, len=%d)", path, err, len(got))
	}
}

func assertInstalled(t *testing.T, dir string, n int) {
	t.Helper()
	ms, err := Installed(dir)
	if err != nil || len(ms) != n {
		t.Fatalf("installed=%v err=%v, want %d", ms, err, n)
	}
}

func TestDownloadFull(t *testing.T) {
	f := newFixture(t, 1<<20+123, func(f *fixture) http.Handler { return serveRange(f) })
	dir := t.TempDir()
	var last Progress
	path, err := f.d.Download(context.Background(), f.m, dir, func(p Progress) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, f.data)
	assertInstalled(t, dir, 1)
	if last.Done != f.m.Size || last.Total != f.m.Size {
		t.Fatalf("final progress %+v", last)
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Fatal("part left behind")
	}
	// Second call is a no-op without the network.
	f.srv.Close()
	if _, err := f.d.Download(context.Background(), f.m, dir, nil); err != nil {
		t.Fatalf("reinstall of verified model: %v", err)
	}
}

func TestDownloadResumeAfterCut(t *testing.T) {
	var calls, ranged atomic.Int32
	f := newFixture(t, 600<<10, func(f *fixture) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Content-Length", "614400")
				w.Write(f.data[:200<<10])
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}
			if r.Header.Get("Range") != "" {
				ranged.Add(1)
			}
			http.ServeContent(w, r, "t.bin", time.Time{}, bytes.NewReader(f.data))
		})
	})
	dir := t.TempDir()
	if _, err := f.d.Download(context.Background(), f.m, dir, nil); err == nil {
		t.Fatal("want error on cut connection")
	}
	assertInstalled(t, dir, 0)
	st, err := os.Stat(filepath.Join(dir, "t.bin.part"))
	if err != nil || st.Size() == 0 || st.Size() >= f.m.Size {
		t.Fatalf("part state: %v %v", st, err)
	}
	path, err := f.d.Download(context.Background(), f.m, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ranged.Load() != 1 {
		t.Fatalf("resume did not use Range (%d)", ranged.Load())
	}
	assertFile(t, path, f.data)
	assertInstalled(t, dir, 1)
}

func TestDownloadServerWithoutRange(t *testing.T) {
	f := newFixture(t, 300<<10, func(f *fixture) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(f.data) })
	})
	dir := t.TempDir()
	// Pre-seed a valid prefix so the client tries to resume.
	if err := os.WriteFile(filepath.Join(dir, "t.bin.part"), f.data[:100<<10], 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := f.d.Download(context.Background(), f.m, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, f.data)
}

func TestDownloadBadRangeResponseRestarts(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, 100<<10, func(f *fixture) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Header.Get("Range") != "" {
				w.Header().Set("Content-Range", "bytes 0-1/2") // lies about the offset
				w.WriteHeader(http.StatusPartialContent)
				return
			}
			w.Write(f.data)
		})
	})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "t.bin.part"), f.data[:1000], 0o644)
	path, err := f.d.Download(context.Background(), f.m, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, f.data)
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestDownloadChecksumMismatch(t *testing.T) {
	f := newFixture(t, 50<<10, func(f *fixture) http.Handler { return serveRange(f) })
	f.m.SHA256 = hex.EncodeToString(make([]byte, 32))
	dir := t.TempDir()
	_, err := f.d.Download(context.Background(), f.m, dir, nil)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err=%v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("leftovers: %v", entries)
	}
	assertInstalled(t, dir, 0)
}

func TestDownloadCancelLeavesOnlyPart(t *testing.T) {
	f := newFixture(t, 600<<10, func(f *fixture) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "614400")
			w.Write(f.data[:100<<10])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
	})
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(300*time.Millisecond, cancel) // after the first chunk has landed
	_, err := f.d.Download(ctx, f.m, dir, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "t.bin")); !os.IsNotExist(err) {
		t.Fatal("final file exists after cancel")
	}
	if _, err := os.Stat(filepath.Join(dir, "t.bin.part")); err != nil {
		t.Fatalf("part missing: %v", err)
	}
	assertInstalled(t, dir, 0)
}

func TestDownloadInsufficientSpace(t *testing.T) {
	var hits atomic.Int32
	f := newFixture(t, 100<<10, func(f *fixture) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(f.data) })
	})
	tests := []struct {
		name string
		free uint64
		ok   bool
	}{
		{"exact without margin", uint64(f.m.Size), false},
		{"just under margin", uint64(f.m.Size) + uint64(f.m.Size)/10 - 1, false},
		{"with margin", uint64(f.m.Size) + uint64(f.m.Size)/10, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.d.FreeSpace = func(string) (uint64, error) { return tt.free, nil }
			dir := t.TempDir()
			before := hits.Load()
			_, err := f.d.Download(context.Background(), f.m, dir, nil)
			if tt.ok != (err == nil) || (!tt.ok && !errors.Is(err, ErrInsufficientSpace)) {
				t.Fatalf("err=%v", err)
			}
			if !tt.ok && hits.Load() != before {
				t.Fatal("network used despite lack of space")
			}
		})
	}
}

func TestDownloadSpaceCountsOnlyRemaining(t *testing.T) {
	f := newFixture(t, 100<<10, func(f *fixture) http.Handler { return serveRange(f) })
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "t.bin.part"), f.data[:90<<10], 0o644)
	f.d.FreeSpace = func(string) (uint64, error) { return 12 << 10, nil } // > 10 KiB * 1.1
	if _, err := f.d.Download(context.Background(), f.m, dir, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadProgressMonotonicAndThrottled(t *testing.T) {
	f := newFixture(t, 2<<20, func(f *fixture) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "2097152")
			for off := 0; off < len(f.data); off += 128 << 10 {
				w.Write(f.data[off : off+128<<10])
				w.(http.Flusher).Flush()
				time.Sleep(15 * time.Millisecond)
			}
		})
	})
	var ps []Progress
	start := time.Now()
	if _, err := f.d.Download(context.Background(), f.m, t.TempDir(), func(p Progress) { ps = append(ps, p) }); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	var prev int64 = -1
	for _, p := range ps {
		if p.Done < prev || p.Done > p.Total || p.Total != f.m.Size {
			t.Fatalf("bad progress sequence at %+v (prev %d)", p, prev)
		}
		prev = p.Done
	}
	if ps[len(ps)-1].Done != f.m.Size {
		t.Fatal("last update is not complete")
	}
	if max := int(elapsed.Seconds()*10) + 4; len(ps) > max {
		t.Fatalf("%d updates in %v, expected <= %d", len(ps), elapsed, max)
	}
	sawRate := false
	for _, p := range ps {
		sawRate = sawRate || p.Rate > 0
	}
	if !sawRate {
		t.Fatal("rate never reported")
	}
}

func TestInstalledIgnoresPartAndUnrecordedFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.bin.part"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.bin"), []byte("x"), 0o644) // renamed but never recorded
	assertInstalled(t, dir, 0)
}

func TestRemove(t *testing.T) {
	f := newFixture(t, 10<<10, func(f *fixture) http.Handler { return serveRange(f) })
	dir := t.TempDir()
	if _, err := f.d.Download(context.Background(), f.m, dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, "nope"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err=%v", err)
	}
	if err := Remove(dir, f.m.ID); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("leftovers %v", entries)
	}
}

func TestExistingFileWithoutRecordIsVerified(t *testing.T) {
	f := newFixture(t, 10<<10, func(f *fixture) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("network used") })
	})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "t.bin"), f.data, 0o644)
	if _, err := f.d.Download(context.Background(), f.m, dir, nil); err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, dir, 1)
}
