package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func appImage(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "Companion-0.0.1-x86_64.AppImage")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAppImageInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("AppImages are Linux only")
	}
	bad := errors.New("does not start")
	tests := []struct {
		name  string
		check func(ctx context.Context, name, path string) error
		want  string
		err   error
	}{
		{"replaces the image", nil, "new image", nil},
		{"check refuses", func(context.Context, string, string) error { return bad }, "old image", bad},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := appImage(t, "old image")
			dl := filepath.Join(t.TempDir(), "download.AppImage")
			if err := os.WriteFile(dl, []byte("new image"), 0o600); err != nil {
				t.Fatal(err)
			}
			a := &AppImage{Path: img, Check: tt.check}
			if err := a.Install(context.Background(), dl); !errors.Is(err, tt.err) {
				t.Fatalf("Install err = %v, want %v", err, tt.err)
			}
			if got := readFile(t, img); got != tt.want {
				t.Errorf("image = %q, want %q", got, tt.want)
			}
			if st, _ := os.Stat(img); st.Mode().Perm()&0o100 == 0 {
				t.Errorf("image not executable: %v", st.Mode())
			}
			entries, _ := os.ReadDir(filepath.Dir(img))
			if want := map[bool]int{true: 2, false: 1}[tt.err == nil]; len(entries) != want {
				t.Errorf("files = %v, want %d", entries, want)
			}
			if tt.err != nil {
				return
			}
			if err := a.Rollback(); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, img); got != "old image" {
				t.Errorf("after rollback image = %q", got)
			}
		})
	}
}

func TestAppImageInstallable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	tests := []struct {
		path string
		err  error
	}{
		{appImage(t, "x"), nil},
		{"/usr/bin/Companion.AppImage", ErrPackageManaged},
		{"/opt/companion-does-not-exist/Companion.AppImage", ErrNotWritable},
	}
	for _, tt := range tests {
		if err := (&AppImage{Path: tt.path}).Installable(); !errors.Is(err, tt.err) && !(tt.err == nil && err == nil) {
			t.Errorf("%s: err = %v, want %v", tt.path, err, tt.err)
		}
	}
}

func TestAppImageFromEnv(t *testing.T) {
	img := appImage(t, "x")
	mount := "/tmp/.mount_CompanXYZ"
	tests := []struct {
		name          string
		exe, img, dir string
		want          string
	}{
		{"running from the image", mount + "/usr/bin/companion", img, mount, img},
		{"not an AppImage", "/home/u/.local/lib/companion/companion", "", "", ""},
		{"exe outside the mount", "/home/u/.local/lib/companion/companion", img, mount, ""},
		{"relative image path", mount + "/usr/bin/companion", "Companion.AppImage", mount, ""},
		{"image missing", mount + "/usr/bin/companion", img + ".gone", mount, ""},
		{"image is a directory", mount + "/usr/bin/companion", filepath.Dir(img), mount, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"APPIMAGE": tt.img, "APPDIR": tt.dir}
			if got := AppImageFromEnv(tt.exe, func(k string) string { return env[k] }); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceInstallsAppImage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("AppImages are Linux only")
	}
	rel := newFakeAsset(t, "0.2.0", "Companion-0.2.0-x86_64.AppImage", []byte("new image"))
	img := appImage(t, "old image")
	s := NewService(Config{
		Checker:    &Checker{Client: rel.srv.Client(), Feed: rel.srv.URL},
		Downloader: &Downloader{Client: rel.srv.Client()},
		Verifier:   &fakeVerifier{},
		Installer:  &AppImage{Path: img},
		Prefs:      newPrefs(),
		Current:    "0.0.1",
		WorkDir:    t.TempDir(),
		GOOS:       "linux", GOARCH: "amd64",
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop(context.Background())
	if st := waitFor(t, s, "release", func(st State) bool { return st.Latest != nil }); st.Managed != "" {
		t.Fatalf("managed = %q", st.Managed)
	}
	s.Install()
	st := waitFor(t, s, "install end", func(st State) bool { return !st.Busy() })
	if st.Phase != PhaseRestartNeeded || st.Err != "" {
		t.Fatalf("phase = %v err = %q", st.Phase, st.Err)
	}
	if got := readFile(t, img); got != "new image" {
		t.Errorf("image = %q", got)
	}
}
