package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/update/updatetest"
)

type memPrefs struct {
	mu   sync.Mutex
	s    settings.Settings
	subs []chan settings.Settings
}

func newPrefs() *memPrefs { return &memPrefs{s: settings.Defaults()} }

func (p *memPrefs) Get() settings.Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.s
}

func (p *memPrefs) Update(_ context.Context, fn func(*settings.Settings)) error {
	p.mu.Lock()
	fn(&p.s)
	s, subs := p.s, p.subs
	p.mu.Unlock()
	for _, c := range subs {
		c <- s
	}
	return nil
}

func (p *memPrefs) Subscribe() (<-chan settings.Settings, func()) {
	c := make(chan settings.Settings, 8)
	p.mu.Lock()
	p.subs = append(p.subs, c)
	p.mu.Unlock()
	return c, func() {}
}

// fakeVerifier records what it was asked to vouch for.
type fakeVerifier struct {
	err  error
	sums []byte
}

func (f *fakeVerifier) VerifyChecksums(_ context.Context, bundle, sums []byte) error {
	if len(bundle) == 0 {
		return ErrNoSignature
	}
	f.sums = sums
	return f.err
}

// fakeRelease serves a feed with one release of version v.
type fakeRelease struct {
	srv     *httptest.Server
	tarball []byte
	sums    string
	noSig   bool
	sig     []byte // the bundle served; "{}" when nil
}

func newFakeRelease(t *testing.T, v string, tarball []byte) *fakeRelease {
	f := &fakeRelease{tarball: tarball}
	name := "companion-" + v + "-linux-x86_64.tar.gz"
	sum := sha256.Sum256(tarball)
	f.sums = hex.EncodeToString(sum[:]) + "  " + name + "\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		assets := []map[string]any{
			{"name": name, "size": len(f.tarball), "browser_download_url": f.srv.URL + "/dl/tarball"},
			{"name": ChecksumsName, "size": len(f.sums), "browser_download_url": f.srv.URL + "/dl/sums"},
		}
		if !f.noSig {
			assets = append(assets, map[string]any{"name": SignatureName, "size": 0, "browser_download_url": f.srv.URL + "/dl/sig"})
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + v, "html_url": f.srv.URL + "/notes", "assets": assets})
	})
	mux.HandleFunc("/dl/tarball", func(w http.ResponseWriter, r *http.Request) { w.Write(f.tarball) })
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(f.sums)) })
	mux.HandleFunc("/dl/sig", func(w http.ResponseWriter, r *http.Request) {
		if f.sig == nil {
			w.Write([]byte("{}"))
			return
		}
		w.Write(f.sig)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func tarballBytes(t *testing.T, core, sidecar string) []byte {
	p := filepath.Join(t.TempDir(), "r.tar.gz")
	writeTarball(t, p, releaseTar(core, sidecar))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func waitFor(t *testing.T, s *Service, what string, ok func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := s.State(); ok(st) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %+v", what, s.State())
	return State{}
}

func TestServiceInstall(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*fakeRelease)
		verifier *fakeVerifier
		restart  bool
		phase    Phase
		code     string
	}{
		{"installs and restarts", nil, &fakeVerifier{}, true, PhaseRestarting, ""},
		{"installs, user restarts", nil, &fakeVerifier{}, false, PhaseRestartNeeded, ""},
		{"bad signature", nil, &fakeVerifier{err: ErrBadSignature}, true, PhaseFailed, "bad_signature"},
		{"no signature asset", func(f *fakeRelease) { f.noSig = true }, &fakeVerifier{}, true, PhaseFailed, "no_signature"},
		{"tarball does not match checksums", func(f *fakeRelease) { f.tarball = append(f.tarball, 0) }, &fakeVerifier{}, true, PhaseFailed, "checksum_mismatch"},
		{"tarball not in checksums", func(f *fakeRelease) {
			f.sums = "0000000000000000000000000000000000000000000000000000000000000000  other.tar.gz\n"
		}, &fakeVerifier{}, true, PhaseFailed, "checksum_missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := newFakeRelease(t, "0.2.0", tarballBytes(t, "new core", "new sidecar"))
			if tt.mutate != nil {
				tt.mutate(rel)
			}
			dir := installDir(t)
			restarted := make(chan struct{}, 1)
			cfg := Config{
				Checker:    &Checker{Client: rel.srv.Client(), Feed: rel.srv.URL},
				Downloader: &Downloader{Client: rel.srv.Client()},
				Verifier:   tt.verifier,
				Installer:  &Installer{Dir: dir, Names: []string{"companion", "stt-sidecar"}},
				Prefs:      newPrefs(),
				Current:    "0.0.1",
				WorkDir:    t.TempDir(),
				GOOS:       "linux", GOARCH: "amd64",
			}
			if tt.restart {
				cfg.Restart = func() { restarted <- struct{}{} }
			}
			s := NewService(cfg)
			if err := s.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer s.Stop(context.Background())
			st := waitFor(t, s, "release", func(st State) bool { return st.Latest != nil })
			if st.Latest.Version.String() != "0.2.0" || st.Dismissed || st.Managed != "" || st.CheckErr != "" || st.LastCheck.IsZero() {
				t.Fatalf("state = %+v", st)
			}
			s.Install()
			st = waitFor(t, s, "install end", func(st State) bool { return !st.Busy() || st.Phase == PhaseRestarting })
			if st.Phase != tt.phase || st.Err != tt.code {
				t.Fatalf("phase = %v err = %q, want %v %q", st.Phase, st.Err, tt.phase, tt.code)
			}
			core := readFile(t, filepath.Join(dir, "companion"))
			if tt.code == "" {
				if core != "new core" || string(tt.verifier.sums) != rel.sums {
					t.Errorf("core = %q, verified %q", core, tt.verifier.sums)
				}
				if tt.restart {
					select {
					case <-restarted:
					case <-time.After(5 * time.Second):
						t.Error("not restarted")
					}
				}
				return
			}
			if core != "old core" {
				t.Errorf("core replaced despite %s", tt.code)
			}
		})
	}
}

func TestServiceChecksAndDismiss(t *testing.T) {
	rel := newFakeRelease(t, "0.2.0", []byte("x"))
	prefs := newPrefs()
	s := NewService(Config{
		Checker: &Checker{Client: rel.srv.Client(), Feed: rel.srv.URL},
		Prefs:   prefs, Current: "0.0.1", GOOS: "linux", GOARCH: "amd64",
	})
	changes := make(chan struct{}, 100)
	s.Watch(func() { changes <- struct{}{} })
	s.Start(context.Background())
	defer s.Stop(context.Background())
	st := waitFor(t, s, "release", func(st State) bool { return st.Latest != nil })
	s.Dismiss(st.Latest.Version)
	if !s.State().Dismissed || prefs.Get().Updates.Dismissed != "0.2.0" {
		t.Fatalf("dismiss: %+v %+v", s.State(), prefs.Get().Updates)
	}
	if len(changes) == 0 {
		t.Error("watchers not notified")
	}
	// A dismissed version stays dismissed across checks.
	before := s.State().LastCheck
	s.CheckNow()
	st = waitFor(t, s, "second check", func(st State) bool { return st.LastCheck.After(before) && !st.Checking })
	if !st.Dismissed {
		t.Error("dismissal lost on recheck")
	}
}

func TestServiceAutoCheckOff(t *testing.T) {
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.NotFound(w, r)
	}))
	defer srv.Close()
	prefs := newPrefs()
	prefs.s.Updates.AutoCheck = false
	s := NewService(Config{Checker: &Checker{Client: srv.Client(), Feed: srv.URL}, Prefs: prefs, Current: "0.0.1", GOOS: "linux", GOARCH: "amd64"})
	s.Start(context.Background())
	defer s.Stop(context.Background())
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if hits != 0 {
		t.Errorf("checked with automatic checks off: %d", hits)
	}
	mu.Unlock()
	// A manual check still works and reports the private/empty feed quietly.
	s.CheckNow()
	st := waitFor(t, s, "manual check", func(st State) bool { return !st.LastCheck.IsZero() && !st.Checking })
	if st.CheckErr != "unavailable" || st.Latest != nil {
		t.Errorf("state = %+v", st)
	}
}

func TestServiceManaged(t *testing.T) {
	tests := []struct {
		name, goos, dir, want string
	}{
		{"package manager", "linux", "/usr/lib/companion", "package_managed"},
		{"no build for the system", "windows", t.TempDir(), "unsupported"},
		{"self-updatable", "linux", t.TempDir(), ""},
	}
	for _, tt := range tests {
		s := NewService(Config{Prefs: newPrefs(), Current: "0.0.1", GOOS: tt.goos, GOARCH: "amd64", Installer: &Installer{Dir: tt.dir}})
		if got := s.State().Managed; got != tt.want {
			t.Errorf("%s: Managed = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// The whole path with the real verifier: a release signed by the release
// workflow on a private Sigstore instance installs; the same release
// signed by any other identity does not.
func TestServiceInstallSigned(t *testing.T) {
	signer, err := updatetest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	rootJSON, err := signer.TrustedRootJSON()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, identity string
		want           string
	}{
		{"release workflow", releaseWorkflow, "new core"},
		{"someone else", "https://github.com/evil/companion/.github/workflows/release.yml@refs/tags/v0.2.0", "old core"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := newFakeRelease(t, "0.2.0", tarballBytes(t, "new core", "new sidecar"))
			if rel.sig, err = signer.SignBundle(tt.identity, ReleaseIssuer, []byte(rel.sums)); err != nil {
				t.Fatal(err)
			}
			v, err := NewSigstoreTestRoot(rootJSON)
			if err != nil {
				t.Fatal(err)
			}
			dir := installDir(t)
			s := NewService(Config{
				Checker:    &Checker{Client: rel.srv.Client(), Feed: rel.srv.URL},
				Downloader: &Downloader{Client: rel.srv.Client()},
				Verifier:   v,
				Installer:  &Installer{Dir: dir, Names: []string{"companion", "stt-sidecar"}},
				Prefs:      newPrefs(), Current: "0.0.1", WorkDir: t.TempDir(), GOOS: "linux", GOARCH: "amd64",
			})
			s.Start(context.Background())
			defer s.Stop(context.Background())
			waitFor(t, s, "release", func(st State) bool { return st.Latest != nil })
			s.Install()
			st := waitFor(t, s, "install end", func(st State) bool { return !st.Busy() })
			if got := readFile(t, filepath.Join(dir, "companion")); got != tt.want {
				t.Errorf("core = %q (phase %v, err %q)", got, st.Phase, st.Err)
			}
		})
	}
}
