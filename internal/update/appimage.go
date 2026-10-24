package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/companion/pkg/contracts"
)

// Target is what an update replaces: the binaries of a tarball install
// (Installer) or a whole AppImage (AppImage).
type Target interface {
	// AssetName is the release file the target installs from.
	AssetName(v Version, goos, goarch string) (string, error)
	// Installable reports whether the target can be updated in place.
	Installable() error
	// Install replaces the target with the downloaded, verified file.
	Install(ctx context.Context, file string) error
}

// AssetName is TarballName: an Installer installs from the tarball.
func (in *Installer) AssetName(v Version, goos, goarch string) (string, error) {
	return TarballName(v, goos, goarch)
}

// AppImageName is the release AppImage for a version and platform.
func AppImageName(v Version, goos, goarch string) (string, error) {
	arch, ok := archName(goarch)
	if goos != "linux" || !ok {
		return "", fmt.Errorf("update: no AppImage for %s/%s: %w", goos, goarch, contracts.Unsupported(CapSelfUpdate))
	}
	return fmt.Sprintf("Companion-%s-%s.AppImage", v, arch), nil
}

// AppImageFromEnv is the AppImage the process runs from, or "" when it
// does not run from one. The runtime sets APPIMAGE to the image file and
// APPDIR to its read-only mount; both must agree with exe so a stray
// variable cannot point the updater at an unrelated file.
func AppImageFromEnv(exe string, getenv func(string) string) string {
	img, dir := getenv("APPIMAGE"), getenv("APPDIR")
	if img == "" || dir == "" || !filepath.IsAbs(img) {
		return ""
	}
	if rel, err := filepath.Rel(dir, exe); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if st, err := os.Stat(img); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	return img
}

// AppImage updates a running AppImage by replacing the image file: the
// binaries inside its mount are read-only. The swap is the Installer's
// (write beside, sync, rename over, previous file kept as .old), and the
// app restarts from Path.
type AppImage struct {
	Path string
	// Check, when set, runs on the new image before it replaces the old
	// one; name is "companion".
	Check func(ctx context.Context, name, path string) error
}

func (a *AppImage) AssetName(v Version, goos, goarch string) (string, error) {
	return AppImageName(v, goos, goarch)
}

func (a *AppImage) swapper() *Installer { return &Installer{Dir: filepath.Dir(a.Path)} }

func (a *AppImage) Installable() error {
	if managedDir(filepath.Dir(a.Path)) {
		return ErrPackageManaged
	}
	return writableDir(filepath.Dir(a.Path))
}

func (a *AppImage) Install(ctx context.Context, file string) error {
	if err := a.Installable(); err != nil {
		return err
	}
	staged, err := a.stage(ctx, file)
	if err != nil {
		return err
	}
	defer os.Remove(staged) // gone already when renamed into place
	if a.Check != nil {
		if err := a.Check(ctx, "companion", staged); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	in := a.swapper()
	if err := in.swap(filepath.Base(a.Path), staged); err != nil {
		return err
	}
	return syncDir(in.Dir)
}

// Rollback puts the image of the last Install back.
func (a *AppImage) Rollback() error {
	in := a.swapper()
	if err := in.restore(filepath.Base(a.Path)); err != nil {
		return err
	}
	return syncDir(in.Dir)
}

// stage copies the download next to Path, where the rename over it is
// atomic; the work directory may be on another filesystem.
func (a *AppImage) stage(ctx context.Context, file string) (string, error) {
	src, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer src.Close()
	f, err := os.CreateTemp(filepath.Dir(a.Path), "."+filepath.Base(a.Path)+".new-*")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotWritable, errors.Unwrap(err))
	}
	p := f.Name()
	_, err = io.Copy(f, &ctxReader{ctx: ctx, r: src})
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
		return "", err
	}
	return p, nil
}
