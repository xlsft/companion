package update

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/settings"
)

// DefaultInterval is how often an automatic check runs after the first.
const DefaultInterval = 6 * time.Hour

// Phase is where an update stands.
type Phase int

const (
	// PhaseIdle: nothing in progress; Latest says whether a release waits.
	PhaseIdle Phase = iota
	PhaseVerifying
	PhaseDownloading
	PhaseInstalling
	// PhaseRestarting: installed, the app is restarting into it.
	PhaseRestarting
	// PhaseRestartNeeded: installed and waiting for the user to restart
	// (RestartNow); CanRestart says whether the app can do it itself.
	PhaseRestartNeeded
	// PhaseFailed: the last attempt failed; Err has the code.
	PhaseFailed
)

// State is a snapshot for the UI.
type State struct {
	Current Version
	// Latest is the newer release when one is available.
	Latest *Release
	// Managed is set when this install cannot update itself: "package_managed",
	// "not_writable" or "unsupported" (Code values); "" when it can.
	Managed string

	Phase       Phase
	Done, Total int64  // download progress
	Err         string // Code of the failed install

	Checking  bool
	LastCheck time.Time // zero before the first finished check
	// CheckErr is the Code of the last check, "" when it succeeded.
	CheckErr string
	// CanRestart: RestartNow restarts into the installed version; false
	// leaves quitting and reopening to the user.
	CanRestart bool
}

// Busy reports an install in progress.
func (s State) Busy() bool {
	return s.Phase == PhaseVerifying || s.Phase == PhaseDownloading || s.Phase == PhaseInstalling || s.Phase == PhaseRestarting
}

// Prefs is the part of the settings service the updater reads and writes.
type Prefs interface {
	Get() settings.Settings
	Update(ctx context.Context, fn func(*settings.Settings)) error
	Subscribe() (<-chan settings.Settings, func())
}

// Config wires a Service.
type Config struct {
	Checker    *Checker
	Downloader *Downloader
	Verifier   SignatureVerifier
	// Installer is the install being updated: an *Installer for a tarball
	// install or an *AppImage.
	Installer Target
	Prefs     Prefs
	// Current is the running version (buildinfo.Version).
	Current string
	// WorkDir holds downloads between attempts.
	WorkDir string
	// Interval between automatic checks; DefaultInterval when zero.
	Interval time.Duration
	// Restart, when set, restarts into the new binaries once the user asks
	// for it (RestartNow); without it the user quits and reopens.
	Restart func()
	// Open shows a URL in the browser (the release notes).
	Open func(ctx context.Context, url string) error
	Log  *slog.Logger
	// GOOS/GOARCH select the release build; the running ones when empty.
	GOOS, GOARCH string
}

// Service checks for releases in the background and installs one on
// request. It implements app.Component.
type Service struct {
	cfg     Config
	current Version
	verErr  error
	kick    chan struct{}

	mu       sync.Mutex
	st       State
	watchers map[int]func()
	nextID   int
	ctx      context.Context // lives from Start to Stop
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func NewService(cfg Config) *Service {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.GOOS == "" {
		cfg.GOOS, cfg.GOARCH = runtime.GOOS, runtime.GOARCH
	}
	s := &Service{cfg: cfg, kick: make(chan struct{}, 1), watchers: map[int]func(){}}
	s.current, s.verErr = ParseVersion(cfg.Current)
	s.st.Current = s.current
	s.st.CanRestart = cfg.Restart != nil
	if cfg.Installer == nil {
		if _, err := TarballName(s.current, cfg.GOOS, cfg.GOARCH); err != nil {
			s.st.Managed = Code(err)
		} else {
			s.st.Managed = Code(ErrNotWritable) // install directory unknown
		}
	} else if _, err := cfg.Installer.AssetName(s.current, cfg.GOOS, cfg.GOARCH); err != nil {
		s.st.Managed = Code(err)
	} else {
		s.st.Managed = Code(cfg.Installer.Installable())
	}
	return s
}

func (s *Service) Name() string { return "update" }

// Start checks once (when automatic checks are on) and then every
// Interval; it returns at once.
func (s *Service) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.ctx, s.cancel = ctx, cancel
	s.mu.Unlock()
	ch, unsub := s.cfg.Prefs.Subscribe()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer unsub()
		s.loop(ctx, ch)
	}()
	return nil
}

func (s *Service) Stop(context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	return nil
}

func (s *Service) loop(ctx context.Context, prefs <-chan settings.Settings) {
	p := s.cfg.Prefs.Get().Updates
	var timer <-chan time.Time
	if p.AutoCheck {
		timer = time.After(0)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer:
			timer = time.After(s.check(ctx))
		case <-s.kick:
			wait := s.check(ctx)
			if s.cfg.Prefs.Get().Updates.AutoCheck {
				timer = time.After(wait)
			}
		case st, ok := <-prefs:
			if !ok {
				prefs = nil
				continue
			}
			n := st.Updates
			switch {
			case !n.AutoCheck:
				timer = nil
			case !p.AutoCheck || n.Prereleases != p.Prereleases:
				// Turned on, or the set of candidate releases changed.
				timer = time.After(0)
			}
			p = n
		}
	}
}

// CheckNow asks for a check whatever the automatic setting.
func (s *Service) CheckNow() {
	select {
	case s.kick <- struct{}{}:
	default: // one is pending already
	}
}

// check asks the feed once and returns the wait until the next check.
func (s *Service) check(ctx context.Context) time.Duration {
	if s.verErr != nil {
		s.update(func(st *State) { st.CheckErr, st.LastCheck = "unsupported", timeNow() })
		return s.cfg.Interval
	}
	s.update(func(st *State) { st.Checking = true })
	prefs := s.cfg.Prefs.Get().Updates
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	r, newer, err := s.cfg.Checker.Newer(cctx, s.current, prefs.Prereleases)
	cancel()
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		s.update(func(st *State) { st.Checking = false })
		return s.cfg.Interval
	}
	wait := s.cfg.Interval
	var rl *RateLimitError
	if errors.As(err, &rl) {
		if d := time.Until(rl.Reset); d > wait {
			wait = d
		}
	}
	if err != nil {
		// Codes only: the error may name the feed's address.
		s.cfg.Log.Info("update check failed", "code", Code(err))
	}
	s.update(func(st *State) {
		st.Checking = false
		st.LastCheck = timeNow()
		st.CheckErr = Code(err)
		if err != nil {
			return // keep what an earlier check found
		}
		if st.Busy() || st.Phase == PhaseRestartNeeded {
			return
		}
		if !newer {
			st.Latest = nil
			return
		}
		if st.Latest == nil || st.Latest.Version != r.Version {
			st.Phase, st.Err = PhaseIdle, ""
		}
		rel := r
		st.Latest = &rel
	})
	return wait
}

// OpenNotes shows the available release's page in the browser.
func (s *Service) OpenNotes() {
	st := s.State()
	if st.Latest == nil || st.Latest.URL == "" || s.cfg.Open == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.cfg.Open(ctx, st.Latest.URL); err != nil {
			s.cfg.Log.Warn("open release notes", "code", Code(err))
		}
	}()
}

// Install downloads, verifies and installs the available release in the
// background. It does nothing while an install runs or before Start.
func (s *Service) Install() {
	s.mu.Lock()
	ctx := s.ctx
	if ctx == nil || s.st.Latest == nil || s.st.Busy() || s.st.Phase == PhaseRestartNeeded {
		s.mu.Unlock()
		return
	}
	rel := *s.st.Latest
	s.st.Phase, s.st.Err, s.st.Done, s.st.Total = PhaseVerifying, "", 0, 0
	s.mu.Unlock()
	s.notify()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.install(ctx, rel)
		if err != nil {
			s.cfg.Log.Warn("update install failed", "code", Code(err))
			s.update(func(st *State) { st.Phase, st.Err = PhaseFailed, Code(err) })
			return
		}
		s.cfg.Log.Info("update installed", "version", rel.Version.String())
		// No restart by itself: the window closing in the middle of a
		// conversation is worse than one more click.
		s.update(func(st *State) { st.Phase = PhaseRestartNeeded })
	}()
}

// RestartNow restarts into the installed update; it does nothing before
// an install finished or when the app cannot restart itself.
func (s *Service) RestartNow() {
	s.mu.Lock()
	if s.st.Phase != PhaseRestartNeeded || s.cfg.Restart == nil {
		s.mu.Unlock()
		return
	}
	s.st.Phase = PhaseRestarting
	s.mu.Unlock()
	s.notify()
	s.cfg.Restart()
}

func (s *Service) install(ctx context.Context, rel Release) error {
	if s.cfg.Installer == nil {
		return ErrNotWritable
	}
	if err := s.cfg.Installer.Installable(); err != nil {
		return err
	}
	name, err := s.cfg.Installer.AssetName(rel.Version, s.cfg.GOOS, s.cfg.GOARCH)
	if err != nil {
		return err
	}
	b, err := selectBundle(rel, name)
	if err != nil {
		return err
	}
	// Trust flows from the signature: checksums.txt is accepted only when
	// signed by the release workflow, the tarball only when it matches.
	sig, err := s.cfg.Downloader.FetchBytes(ctx, b.Signature, 1<<20)
	if err != nil {
		return err
	}
	sums, err := s.cfg.Downloader.FetchBytes(ctx, b.Checksums, 1<<20)
	if err != nil {
		return err
	}
	if s.cfg.Verifier == nil {
		return ErrBadSignature
	}
	if err := s.cfg.Verifier.VerifyChecksums(ctx, sig, sums); err != nil {
		return err
	}
	cs, err := ParseChecksums(sums)
	if err != nil {
		return err
	}
	if _, ok := cs[b.Tarball.Name]; !ok {
		return ErrChecksumMissing
	}
	if err := os.MkdirAll(s.cfg.WorkDir, 0o700); err != nil {
		return err
	}
	tarball := filepath.Join(s.cfg.WorkDir, b.Tarball.Name)
	s.update(func(st *State) { st.Phase = PhaseDownloading })
	var last time.Time
	err = s.cfg.Downloader.Fetch(ctx, b.Tarball, tarball, func(done, total int64) {
		// The UI needs a few updates a second, not one per buffer.
		if now := time.Now(); now.Sub(last) > 100*time.Millisecond || done == total {
			last = now
			s.update(func(st *State) { st.Done, st.Total = done, total })
		}
	})
	if err != nil {
		return err
	}
	defer os.Remove(tarball)
	s.update(func(st *State) { st.Phase = PhaseVerifying })
	if err := cs.VerifyFile(ctx, tarball, b.Tarball.Name); err != nil {
		return err
	}
	s.update(func(st *State) { st.Phase = PhaseInstalling })
	return s.cfg.Installer.Install(ctx, tarball)
}

// State returns a snapshot.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	if st.Latest != nil {
		r := *st.Latest
		st.Latest = &r
	}
	return st
}

// Watch calls fn after every state change until the returned cancel.
func (s *Service) Watch(fn func()) func() {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.watchers[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.watchers, id)
		s.mu.Unlock()
	}
}

func (s *Service) update(fn func(*State)) {
	s.mu.Lock()
	fn(&s.st)
	s.mu.Unlock()
	s.notify()
}

func (s *Service) notify() {
	s.mu.Lock()
	fns := make([]func(), 0, len(s.watchers))
	for _, fn := range s.watchers {
		fns = append(fns, fn)
	}
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}
