package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// maxBinary bounds one extracted binary; the whisper sidecar is the
// largest at a few tens of MB.
const maxBinary = 1 << 30

// Installer replaces the app's binaries in Dir with those of a release
// tarball. Each file is written beside its target, synced and renamed
// over it, so a crash leaves either the old or the new file, never half
// of one; the previous binaries stay as <name>.old for Rollback.
type Installer struct {
	Dir string
	// Names are the binaries to replace; DefaultBinaries when nil. All
	// must be in the archive.
	Names []string
	// Check, when set, runs on each extracted file before anything is
	// replaced, e.g. starting the new core with -version.
	Check func(ctx context.Context, name, path string) error
}

// DefaultBinaries are the files of the release tarball the app replaces.
func DefaultBinaries() []string {
	if runtime.GOOS == "windows" {
		return []string{"companion.exe", "stt-sidecar.exe"}
	}
	return []string{"companion", "stt-sidecar"}
}

// InstallDir is the directory of the running executable exe, symlinks
// resolved, so ~/.local/bin/companion → ~/.local/lib/companion updates
// the real files.
func InstallDir(exe string) (string, error) {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Dir(real), nil
}

// managedPrefixes hold files that a package manager owns. /usr/local is
// the administrator's, not a package's, and is left to the write check.
var managedPrefixes = []string{"/usr/", "/bin/", "/sbin/", "/lib/", "/nix/", "/gnu/store/", "/snap/", "/var/lib/flatpak/", "/app/"}

func (in *Installer) names() []string {
	if in.Names == nil {
		return DefaultBinaries()
	}
	return in.Names
}

// Installable reports whether Dir can be updated in place.
func (in *Installer) Installable() error {
	if runtime.GOOS != "windows" {
		d := filepath.ToSlash(filepath.Clean(in.Dir)) + "/"
		for _, p := range managedPrefixes {
			if strings.HasPrefix(d, p) && !strings.HasPrefix(d, "/usr/local/") {
				return ErrPackageManaged
			}
		}
	}
	f, err := os.CreateTemp(in.Dir, ".companion-write-check-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, errors.Unwrap(err))
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// Install extracts the binaries from the gzipped tarball and swaps them in.
func (in *Installer) Install(ctx context.Context, tarball string) (err error) {
	if err := in.Installable(); err != nil {
		return err
	}
	staged, err := in.extract(ctx, tarball)
	defer func() {
		for _, p := range staged {
			os.Remove(p) // gone already when renamed into place
		}
	}()
	if err != nil {
		return err
	}
	if in.Check != nil {
		for _, n := range in.names() {
			if err := in.Check(ctx, n, staged[n]); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var done []string
	for _, n := range in.names() {
		if err := in.swap(n, staged[n]); err != nil {
			// Put back what was already replaced: the pair must match.
			for _, d := range done {
				in.restore(d)
			}
			return err
		}
		done = append(done, n)
	}
	return syncDir(in.Dir)
}

// Rollback puts the .old binaries of the last Install back.
func (in *Installer) Rollback() error {
	var errs []error
	for _, n := range in.names() {
		if _, err := os.Stat(in.old(n)); err != nil {
			continue
		}
		errs = append(errs, in.restore(n))
	}
	errs = append(errs, syncDir(in.Dir))
	return errors.Join(errs...)
}

func (in *Installer) target(n string) string { return filepath.Join(in.Dir, n) }
func (in *Installer) old(n string) string    { return filepath.Join(in.Dir, n+".old") }

func (in *Installer) restore(n string) error {
	return os.Rename(in.old(n), in.target(n))
}

// swap keeps the current file as .old and moves the staged one over it.
// A hard link keeps .old while the rename replaces the target atomically;
// where links are unavailable (or Windows, which cannot rename over a
// running executable) the current file is renamed away first.
func (in *Installer) swap(n, staged string) error {
	cur, old := in.target(n), in.old(n)
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(cur); errors.Is(err, os.ErrNotExist) {
		return os.Rename(staged, cur)
	}
	if runtime.GOOS != "windows" && os.Link(cur, old) == nil {
		if err := os.Rename(staged, cur); err != nil {
			os.Remove(old)
			return err
		}
		return nil
	}
	if err := os.Rename(cur, old); err != nil {
		return err
	}
	if err := os.Rename(staged, cur); err != nil {
		os.Rename(old, cur)
		return err
	}
	return nil
}

// extract writes each wanted binary of the archive to a temp file in Dir.
// Archive paths are matched by name only (at the top or one directory
// down), so no entry can write outside Dir.
func (in *Installer) extract(ctx context.Context, tarball string) (map[string]string, error) {
	f, err := os.Open(tarball)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(&ctxReader{ctx: ctx, r: f})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrBadArchive, err)
	}
	want := map[string]bool{}
	for _, n := range in.names() {
		want[n] = true
	}
	staged := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return staged, ctx.Err()
			}
			return staged, fmt.Errorf("%w: %v", ErrBadArchive, err)
		}
		n, ok := binaryName(h.Name, want)
		if !ok || h.Typeflag != tar.TypeReg {
			continue
		}
		if _, dup := staged[n]; dup {
			return staged, fmt.Errorf("%w: %s twice", ErrBadArchive, n)
		}
		p, err := in.stage(n, tr, h.Size)
		if err != nil {
			if ctx.Err() != nil {
				return staged, ctx.Err()
			}
			return staged, err
		}
		staged[n] = p
	}
	for n := range want {
		if _, ok := staged[n]; !ok {
			return staged, fmt.Errorf("%w: %s missing", ErrBadArchive, n)
		}
	}
	return staged, nil
}

// binaryName matches "companion" or "<dir>/companion".
func binaryName(name string, want map[string]bool) (string, bool) {
	name = path.Clean(strings.TrimPrefix(name, "./"))
	dir, base := path.Split(name)
	dir = strings.TrimSuffix(dir, "/")
	if !want[base] || strings.Contains(dir, "/") || dir == ".." || path.IsAbs(name) {
		return "", false
	}
	return base, true
}

// syncDir makes the renames in dir durable. Windows has no directory
// fsync; NTFS journals the rename itself.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

func (in *Installer) stage(n string, r io.Reader, size int64) (string, error) {
	if size < 0 || size > maxBinary {
		return "", fmt.Errorf("%w: %s too large", ErrBadArchive, n)
	}
	f, err := os.CreateTemp(in.Dir, "."+n+".new-*")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotWritable, errors.Unwrap(err))
	}
	p := f.Name()
	_, err = io.CopyN(f, r, size)
	if err == nil {
		err = f.Chmod(0o755)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(p)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "", fmt.Errorf("%w: %s truncated", ErrBadArchive, n)
		}
		return "", err
	}
	return p, nil
}
