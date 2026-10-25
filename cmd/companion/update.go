package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/buildinfo"
	"github.com/xlsft/companion/internal/platform/desktop"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/update"
)

// Environment of the updater. GITHUB_TOKEN is optional and only raises
// GitHub's rate limit. The others are development and test hooks for a
// local release server (cmd/spikes/update): COMPANION_UPDATE_URL points
// the feed (a GitHub repository API base, e.g.
// http://127.0.0.1:8765/repos/xlsft/companion) at it, and
// COMPANION_UPDATE_TRUSTED_ROOT names the trusted_root.json of the private
// Sigstore instance that signs its releases (the release identity stays
// pinned). COMPANION_UPDATE_AUTOINSTALL=1 installs what that local feed
// offers without waiting for the Update button.
const (
	envUpdateURL   = "COMPANION_UPDATE_URL"
	envTrustedRoot = "COMPANION_UPDATE_TRUSTED_ROOT"
	envAutoInstall = "COMPANION_UPDATE_AUTOINSTALL"
	envToken       = "GITHUB_TOKEN"
)

// newUpdater wires the in-app updater for the binaries next to exe.
// restart is called once an update is installed; nil leaves restarting
// to the user.
func newUpdater(log *slog.Logger, dataDir string, svc *settings.Service, exe string, restart func()) *update.Service {
	client := &http.Client{Timeout: 30 * time.Minute} // bounds a stalled tarball download
	ua := "companion/" + buildinfo.Version
	token := os.Getenv(envToken)
	var inst update.Target // an interface: no typed nil when the directory is unknown
	if img := update.AppImageFromEnv(exe, os.Getenv); img != "" {
		inst = &update.AppImage{Path: img, Check: smokeCheck}
	} else if dir, err := update.InstallDir(exe); err == nil {
		inst = &update.Installer{Dir: dir, Check: smokeCheck}
	} else {
		log.Warn("update: install directory unknown", "err", err)
	}
	var verifier update.SignatureVerifier = update.NewSigstore(filepath.Join(dataDir, "sigstore"), client)
	if p := os.Getenv(envTrustedRoot); p != "" {
		// Unusable test root: keep the public-good one, which refuses
		// test signatures, rather than accept anything.
		if b, err := os.ReadFile(p); err != nil {
			log.Warn("update: test trusted root unreadable", "err", err)
		} else if v, err := update.NewSigstoreTestRoot(b); err != nil {
			log.Warn("update: test trusted root invalid", "err", err)
		} else {
			log.Warn("update: verifying releases against a test trusted root")
			verifier = v
		}
	}
	s := update.NewService(update.Config{
		Checker:    &update.Checker{Client: client, Feed: os.Getenv(envUpdateURL), Token: token, UserAgent: ua},
		Downloader: &update.Downloader{Client: client, Token: token, UserAgent: ua},
		Verifier:   verifier,
		Installer:  inst,
		Prefs:      svc,
		Current:    buildinfo.Version,
		WorkDir:    filepath.Join(dataDir, "updates"),
		Restart:    restart,
		Open:       desktop.OpenURL,
		Log:        log,
	})
	// Test hook for a click-free end-to-end run: install whatever the
	// local feed offers as soon as it is found. Ignored without the local
	// feed, so a real release is never installed unasked.
	if os.Getenv(envAutoInstall) == "1" && os.Getenv(envUpdateURL) != "" {
		var install, restart sync.Once
		s.Watch(func() {
			switch st := s.State(); {
			case st.Latest != nil && st.Phase == update.PhaseIdle:
				install.Do(func() { go s.Install() })
			case st.Phase == update.PhaseRestartNeeded:
				// Nobody is there to press Restart.
				restart.Do(func() { go s.RestartNow() })
			}
		})
	}
	return s
}

// smokeCheck runs the new core with -version before it replaces the
// running one, so a binary that cannot even start is never installed.
func smokeCheck(ctx context.Context, name, path string) error {
	if name != "companion" && name != "companion.exe" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil || !bytes.HasPrefix(out, []byte("openclick ")) {
		return fmt.Errorf("%w: new core does not start", update.ErrBadArchive)
	}
	return nil
}
