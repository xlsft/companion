package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xlsft/companion/internal/doubletap"
	"github.com/xlsft/companion/pkg/contracts"
)

// connTimeout bounds one client connection; ctl sends a single line.
const connTimeout = 5 * time.Second

// ErrInUse means another live process already serves the socket.
var ErrInUse = errors.New("control: socket in use by another instance")

// DefaultPath is $XDG_RUNTIME_DIR/openclick/control.sock. The runtime dir
// is per user, mode 0700 and cleared at logout, which is exactly the
// lifetime and visibility the socket needs.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", errors.New("control: XDG_RUNTIME_DIR is not set")
	}
	return filepath.Join(dir, "openclick", "control.sock"), nil
}

// Server listens on the control socket and implements
// contracts.HotkeyService for the bindings registered with it.
//
// SupportsRelease starts false: compositor binds normally fire on press
// only (niri has no release binds). It turns true once a "release" command
// arrives, which only a user who configured release binds (Hyprland bindr,
// Sway --release) sends.
type Server struct {
	ln       *net.UnixListener
	path     string
	allowUID uint32

	mu   sync.Mutex
	ids  map[contracts.HotkeyID]bool
	open map[*net.UnixConn]struct{}
	shut bool

	// tap counts "tap" commands toward a double tap of its key for tapID;
	// nil while double tap is off.
	tap   *doubletap.Detector
	tapID contracts.HotkeyID
	now   func() time.Time

	sawRelease atomic.Bool
	events     chan contracts.HotkeyEvent
	conns      sync.WaitGroup
	done       chan struct{}
	closeOnce  sync.Once
}

// Listen creates the socket at path (mode 0600 in a 0700 directory) and
// starts accepting. A stale socket left by a crashed process is replaced;
// a live one yields ErrInUse.
func Listen(path string) (*Server, error) {
	if !peerCredSupported {
		return nil, contracts.Unsupported(contracts.CapGlobalHotkey)
	}
	uid := uint32(os.Getuid())
	if err := prepareDir(filepath.Dir(path), uid); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			c.Close()
			return nil, ErrInUse
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("control: remove stale socket: %w", err)
		}
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("control: listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("control: chmod socket: %w", err)
	}
	s := newServer(uid)
	s.ln, s.path = ln, path
	go s.accept()
	return s, nil
}

func newServer(uid uint32) *Server {
	return &Server{
		allowUID: uid,
		ids:      map[contracts.HotkeyID]bool{},
		open:     map[*net.UnixConn]struct{}{},
		events:   make(chan contracts.HotkeyEvent, 32),
		now:      time.Now,
		done:     make(chan struct{}),
	}
}

// prepareDir makes sure the socket directory is ours and private, because
// the directory mode is what keeps other users from even connecting.
func prepareDir(dir string, uid uint32) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("control: create dir: %w", err)
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("control: stat dir: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("control: %s is not a directory", dir)
	}
	if owner, ok := fileOwner(st); ok && owner != uid {
		return fmt.Errorf("control: %s is owned by uid %d", dir, owner)
	}
	if st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("control: chmod dir: %w", err)
		}
	}
	return nil
}

// Path is the socket path the server listens on.
func (s *Server) Path() string { return s.path }

func (s *Server) accept() {
	defer close(s.done)
	for {
		c, err := s.ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				s.conns.Wait()
				close(s.events)
				return
			}
			continue
		}
		s.conns.Add(1)
		go func() {
			defer s.conns.Done()
			s.serveConn(c)
		}()
	}
}

func (s *Server) serveConn(c *net.UnixConn) {
	s.mu.Lock()
	if s.shut {
		s.mu.Unlock()
		c.Close()
		return
	}
	s.open[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.open, c)
		s.mu.Unlock()
		c.Close()
	}()
	_ = c.SetDeadline(time.Now().Add(connTimeout))
	w := bufio.NewWriter(c)
	reply := func(code string) {
		if code == "" {
			_, _ = w.WriteString("ok\n")
		} else {
			_, _ = w.WriteString("err " + code + "\n")
		}
		_ = w.Flush() // a client that hung up gets no reply
	}
	// The socket mode already limits access to our uid; the credential
	// check also covers root and a socket reached through a bind mount.
	uid, err := peerUID(c)
	if err != nil || uid != s.allowUID {
		reply(CodeForbidden)
		return
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, maxLine), maxLine)
	for sc.Scan() {
		reply(s.handle(sc.Text()))
	}
	if errors.Is(sc.Err(), bufio.ErrTooLong) {
		reply(CodeTooLong)
	}
}

// handle runs one command and returns the reply error code, "" for ok.
func (s *Server) handle(line string) string {
	cmd, err := ParseLine(line)
	if err != nil {
		var pe *ProtocolError
		if errors.As(err, &pe) {
			return pe.Code
		}
		return CodeBadCommand
	}
	if cmd.Verb == VerbPing {
		return ""
	}
	if cmd.Verb == VerbTap {
		return s.handleTap(cmd.Key)
	}
	s.mu.Lock()
	known := s.ids[cmd.ID]
	s.mu.Unlock()
	if !known {
		return CodeUnknownBinding
	}
	if cmd.Action == ActionRelease {
		s.sawRelease.Store(true)
	}
	select {
	case s.events <- contracts.HotkeyEvent{ID: cmd.ID, Kind: cmd.Action.Kind()}:
		return ""
	default:
		return CodeBusy
	}
}

// handleTap counts one clean tap of key. A tap of a key that is not set
// (a bind left from an earlier setting) is refused like an unknown binding.
func (s *Server) handleTap(key string) string {
	s.mu.Lock()
	det, id := s.tap, s.tapID
	if det == nil || det.Key() != key {
		s.mu.Unlock()
		return CodeUnknownBinding
	}
	fired := det.Tap(s.now())
	s.mu.Unlock()
	if !fired {
		return ""
	}
	select {
	case s.events <- contracts.HotkeyEvent{ID: id, Kind: contracts.HotkeyToggled}:
		return ""
	default:
		return CodeBusy
	}
}

// SetDoubleTap makes two quick "tap key" commands toggle id; "" stops
// counting. Whether taps arrive at all is up to the compositor's binds
// (internal/platform/keybind). It implements contracts.DoubleTapper.
func (s *Server) SetDoubleTap(ctx context.Context, id contracts.HotkeyID, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := doubletap.Check(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tap, s.tapID = nil, id
	if key != "" {
		s.tap = doubletap.New(key)
	}
	return nil
}

// Register accepts every binding: the chord is whatever the user binds in
// the compositor, so only the ID matters here.
func (s *Server) Register(ctx context.Context, bindings []contracts.HotkeyBinding) ([]contracts.HotkeyBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.ids)
	for _, b := range bindings {
		s.ids[b.ID] = true
	}
	return append([]contracts.HotkeyBinding(nil), bindings...), nil
}

func (s *Server) Unregister(ctx context.Context) error {
	s.mu.Lock()
	clear(s.ids)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *Server) SupportsRelease() bool { return s.sawRelease.Load() }

func (s *Server) Events() <-chan contracts.HotkeyEvent { return s.events }

// Close stops listening, removes the socket and closes Events once open
// connections finished.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.ln.Close()
		s.mu.Lock()
		s.shut = true
		for c := range s.open {
			c.Close()
		}
		s.mu.Unlock()
		<-s.done
	})
	return err
}
