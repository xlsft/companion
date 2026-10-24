package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type tarEntry struct {
	name, body string
	link       bool // a symlink to body instead of a file
}

func writeTarball(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		switch {
		case e.link:
			h = &tar.Header{Name: e.name, Linkname: e.body, Typeflag: tar.TypeSymlink}
		case strings.HasSuffix(e.name, "/"):
			h = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if !e.link {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
}

// releaseTar is the layout of the published tarball.
func releaseTar(core, sidecar string) []tarEntry {
	d := "companion-0.2.0-linux-x86_64/"
	return []tarEntry{
		{name: d, body: ""},
		{name: d + "companion", body: core},
		{name: d + "stt-sidecar", body: sidecar},
		{name: d + "install.sh", body: "#!/bin/sh"},
		{name: d + "share/applications/companion", body: "not the binary"},
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func installDir(t *testing.T) string {
	dir := t.TempDir()
	for n, body := range map[string]string{"companion": "old core", "stt-sidecar": "old sidecar"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInstallReplacesAndRollsBack(t *testing.T) {
	dir := installDir(t)
	tarball := filepath.Join(t.TempDir(), "r.tar.gz")
	writeTarball(t, tarball, releaseTar("new core", "new sidecar"))
	in := &Installer{Dir: dir, Names: []string{"companion", "stt-sidecar"}}
	if err := in.Install(context.Background(), tarball); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]string{
		"companion": "new core", "stt-sidecar": "new sidecar",
		"companion.old": "old core", "stt-sidecar.old": "old sidecar",
	} {
		if got := readFile(t, filepath.Join(dir, n)); got != want {
			t.Errorf("%s = %q, want %q", n, got, want)
		}
	}
	if st, _ := os.Stat(filepath.Join(dir, "companion")); runtime.GOOS != "windows" && st.Mode().Perm()&0o100 == 0 {
		t.Errorf("new core not executable: %v", st.Mode())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 4 {
		t.Errorf("stray files left: %v", entries)
	}
	if err := in.Rollback(); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]string{"companion": "old core", "stt-sidecar": "old sidecar"} {
		if got := readFile(t, filepath.Join(dir, n)); got != want {
			t.Errorf("after rollback %s = %q, want %q", n, got, want)
		}
	}
}

func TestInstallRefusesBadArchives(t *testing.T) {
	tests := []struct {
		name    string
		entries []tarEntry
		check   func(context.Context, string, string) error
		err     error
	}{
		{"sidecar missing", releaseTar("new core", "")[:2], nil, ErrBadArchive},
		{"binary as symlink", []tarEntry{{name: "companion", body: "/bin/sh", link: true}, {name: "stt-sidecar", body: "x"}}, nil, ErrBadArchive},
		{"nested too deep", []tarEntry{{name: "a/b/companion", body: "x"}, {name: "stt-sidecar", body: "x"}}, nil, ErrBadArchive},
		{"escaping path", []tarEntry{{name: "../companion", body: "x"}, {name: "stt-sidecar", body: "x"}}, nil, ErrBadArchive},
		{"listed twice", []tarEntry{{name: "companion", body: "x"}, {name: "d/companion", body: "y"}, {name: "stt-sidecar", body: "x"}}, nil, ErrBadArchive},
		{"smoke check fails", releaseTar("new core", "new sidecar"), func(_ context.Context, n, p string) error {
			if n == "companion" {
				return ErrChecksumMismatch
			}
			return nil
		}, ErrChecksumMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := installDir(t)
			tarball := filepath.Join(t.TempDir(), "r.tar.gz")
			writeTarball(t, tarball, tt.entries)
			in := &Installer{Dir: dir, Names: []string{"companion", "stt-sidecar"}, Check: tt.check}
			if err := in.Install(context.Background(), tarball); !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if got := readFile(t, filepath.Join(dir, "companion")); got != "old core" {
				t.Errorf("core touched: %q", got)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 2 {
				t.Errorf("stray files left: %v", entries)
			}
		})
	}
	t.Run("not gzip", func(t *testing.T) {
		dir := installDir(t)
		p := filepath.Join(t.TempDir(), "r.tar.gz")
		os.WriteFile(p, []byte("plain"), 0o600)
		if err := (&Installer{Dir: dir}).Install(context.Background(), p); !errors.Is(err, ErrBadArchive) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestInstallable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths and permissions")
	}
	ro := t.TempDir()
	os.Chmod(ro, 0o500)
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	tests := []struct {
		dir string
		err error
	}{
		{t.TempDir(), nil},
		{"/usr/lib/companion", ErrPackageManaged},
		{"/usr/bin", ErrPackageManaged},
		{"/nix/store/abc-companion/bin", ErrPackageManaged},
		{"/usr/local/lib/companion-does-not-exist", ErrNotWritable},
		{ro, ErrNotWritable},
	}
	for _, tt := range tests {
		if tt.dir == ro && os.Geteuid() == 0 {
			continue // root writes anywhere
		}
		err := (&Installer{Dir: tt.dir}).Installable()
		if (tt.err == nil && err != nil) || (tt.err != nil && !errors.Is(err, tt.err)) {
			t.Errorf("%s: err = %v, want %v", tt.dir, err, tt.err)
		}
	}
}

func TestInstallDirFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges")
	}
	lib := installDir(t)
	bin := t.TempDir()
	link := filepath.Join(bin, "companion")
	if err := os.Symlink(filepath.Join(lib, "companion"), link); err != nil {
		t.Fatal(err)
	}
	got, err := InstallDir(link)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(lib)
	if got != want {
		t.Errorf("InstallDir = %s, want %s", got, want)
	}
}
