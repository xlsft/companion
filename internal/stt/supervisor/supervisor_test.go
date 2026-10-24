package supervisor

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/stt/protocol"
)

var sidecarBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stt-supervisor-test")
	if err == nil {
		bin := filepath.Join(dir, "stt-sidecar")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		build := exec.Command("go", "build", "-o", bin, "github.com/xlsft/companion/cmd/stt-sidecar")
		build.Stderr = os.Stderr
		if build.Run() == nil {
			sidecarBin = bin
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func processLauncher(t *testing.T, args ...string) (Launcher, *atomic.Int32) {
	t.Helper()
	if sidecarBin == "" {
		t.Skip("could not build cmd/stt-sidecar")
	}
	var n atomic.Int32
	return func(ctx context.Context) (*stt.Client, error) {
		n.Add(1)
		return stt.Start(ctx, stt.Config{
			Path: sidecarBin,
			Args: append([]string{"-engine", "fake"}, args...),
			Init: protocol.InitializeParams{Language: "auto", Backend: "cpu"},
		})
	}, &n
}

// stub is an in-process peer that answers initialize and, depending on
// mode, health; it lets tests produce hangs and instant deaths that the
// real fake engine cannot.
type stubMode int

const (
	stubOK stubMode = iota
	stubIgnoreHealth
	stubDieAfterInit
)

func stubLauncher(mode stubMode, n *atomic.Int32) Launcher {
	return func(ctx context.Context) (*stt.Client, error) {
		n.Add(1)
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		go func() {
			defer outW.Close()
			enc := protocol.NewEncoder(outW)
			dec := protocol.NewDecoder(inR)
			for {
				m, err := dec.Decode()
				if err != nil {
					if protocol.IsTerminal(err) {
						return
					}
					continue
				}
				if !m.IsRequest() {
					continue
				}
				var res any = struct{}{}
				switch m.Method {
				case protocol.MethodInitialize:
					res = protocol.InitializeResult{ProtocolVersion: "1", Engine: "stub"}
				case protocol.MethodHealth:
					if mode == stubIgnoreHealth {
						continue
					}
					res = protocol.HealthResult{OK: true, Initialized: true}
				}
				r, _ := protocol.NewResult(m.ID, res)
				_ = enc.Encode(r)
				if m.Method == protocol.MethodInitialize && mode == stubDieAfterInit {
					return
				}
				if m.Method == protocol.MethodShutdown {
					return
				}
			}
		}()
		c := stt.NewClient(outR, inW, nil)
		if _, err := c.Initialize(ctx, protocol.InitializeParams{}); err != nil {
			_ = inW.Close()
			return nil, err
		}
		return c, nil
	}
}

func fastCfg(l Launcher) Config {
	return Config{
		Launcher:       l,
		HealthInterval: 20 * time.Millisecond,
		HealthTimeout:  60 * time.Millisecond,
		BackoffInitial: 5 * time.Millisecond,
		BackoffMax:     40 * time.Millisecond,
		MaxRestarts:    3,
		CrashWindow:    time.Minute,
		StopTimeout:    time.Second,
	}
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitState(t *testing.T, ch <-chan State, want Kind) State {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case st, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed waiting for %v", want)
			}
			if st.Kind == want {
				return st
			}
		case <-timeout:
			t.Fatalf("timeout waiting for %v", want)
		}
	}
}

func tone(d time.Duration) []int16 {
	out := make([]int16, int(d.Seconds()*protocol.SampleRate))
	for i := range out {
		out[i] = int16(8000 * math.Sin(2*math.Pi*220*float64(i)/protocol.SampleRate))
	}
	return out
}

func stopAndCheckLeaks(t *testing.T, s *Supervisor, base int) {
	t.Helper()
	if err := s.Stop(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("goroutines %d > %d after Stop\n%s", n, base, buf)
	}
}

func TestBackoff(t *testing.T) {
	c := Config{BackoffInitial: 500 * time.Millisecond, BackoffMax: 30 * time.Second}
	for n, want := range map[int]time.Duration{1: 500 * time.Millisecond, 2: time.Second, 3: 2 * time.Second, 7: 30 * time.Second, 50: 30 * time.Second} {
		if got := c.Backoff(n); got != want {
			t.Errorf("Backoff(%d)=%v want %v", n, got, want)
		}
	}
}

func TestReadySessionAndCleanStop(t *testing.T) {
	l, n := processLauncher(t)
	base := runtime.NumGoroutine()
	s := New(fastCfg(l))
	s.Start(context.Background())
	ctx := testCtx(t)
	if err := s.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	// Several health ticks must not disturb a healthy process.
	time.Sleep(100 * time.Millisecond)
	if n.Load() != 1 {
		t.Fatalf("launches = %d", n.Load())
	}
	ss, err := s.StartSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.Write(tone(time.Second)); err != nil {
		t.Fatal(err)
	}
	if f, err := ss.Finish(ctx); err != nil || f.Text == "" {
		t.Fatalf("final %+v %v", f, err)
	}
	stopAndCheckLeaks(t, s, base)
	if _, err := s.StartSession(ctx); !errors.Is(err, ErrStopped) {
		t.Fatalf("after stop: %v", err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCrashDuringFinalInterruptsAndRestarts(t *testing.T) {
	l, n := processLauncher(t, "-fake-exit-on-final")
	base := runtime.NumGoroutine()
	s := New(fastCfg(l))
	states, cancel := s.Subscribe()
	defer cancel()
	s.Start(context.Background())
	ctx := testCtx(t)
	waitState(t, states, Ready)
	first, _ := s.Client()

	ss, err := s.StartSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.Write(tone(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = ss.Finish(ctx)
	if !errors.Is(err, ErrInterrupted) || !errors.Is(err, stt.ErrSidecarExited) {
		t.Fatalf("finish err = %v", err)
	}
	if err := ss.Write(tone(10 * time.Millisecond)); err == nil {
		t.Fatal("write after crash succeeded")
	}
	st := waitState(t, states, Restarting)
	if st.Attempt != 1 || st.Reason != ReasonSidecarExited {
		t.Fatalf("restarting %+v", st)
	}
	waitState(t, states, Ready)
	second, err := s.Client()
	if err != nil || second == first || n.Load() != 2 {
		t.Fatalf("client replaced=%v err=%v launches=%d", second != first, err, n.Load())
	}
	// The new process serves a session (it would crash again only at final).
	if _, err := s.StartSession(ctx); err != nil {
		t.Fatal(err)
	}
	stopAndCheckLeaks(t, s, base)
}

func TestHealthTimeoutRestarts(t *testing.T) {
	var n atomic.Int32
	base := runtime.NumGoroutine()
	s := New(fastCfg(stubLauncher(stubIgnoreHealth, &n)))
	states, cancel := s.Subscribe()
	defer cancel()
	s.Start(context.Background())
	waitState(t, states, Ready)
	st := waitState(t, states, Restarting)
	if st.Reason != ReasonHealthTimeout {
		t.Fatalf("reason %q", st.Reason)
	}
	if _, err := s.Client(); !errors.Is(err, ErrNotReady) {
		t.Fatalf("client during restart: %v", err)
	}
	waitState(t, states, Ready)
	if n.Load() < 2 {
		t.Fatalf("launches = %d", n.Load())
	}
	stopAndCheckLeaks(t, s, base)
}

func TestCrashLoopDegradesAndResetRecovers(t *testing.T) {
	var n atomic.Int32
	var mode atomic.Int32 // stubMode
	mode.Store(int32(stubDieAfterInit))
	l := func(ctx context.Context) (*stt.Client, error) {
		return stubLauncher(stubMode(mode.Load()), &n)(ctx)
	}
	base := runtime.NumGoroutine()
	s := New(fastCfg(l))
	states, cancel := s.Subscribe()
	defer cancel()
	s.Start(context.Background())
	st := waitState(t, states, Degraded)
	// The stub exits right after answering initialize; depending on
	// scheduling the client sees the exit before or after Initialize
	// returns, so the last failure is either one. Both are crashes.
	if st.Reason != ReasonCrashLoop || (st.LastFailure != ReasonSidecarExited && st.LastFailure != ReasonLaunchFailed) {
		t.Fatalf("degraded %+v", st)
	}
	// MaxRestarts=3 restarts, so the 4th failure degrades: 4 launches.
	if n.Load() != 4 {
		t.Fatalf("launches = %d", n.Load())
	}
	if _, err := s.Client(); !errors.Is(err, ErrDegraded) {
		t.Fatalf("client: %v", err)
	}
	if _, err := s.StartSession(testCtx(t)); !errors.Is(err, ErrDegraded) {
		t.Fatalf("session: %v", err)
	}
	// No retries while degraded.
	time.Sleep(150 * time.Millisecond)
	if n.Load() != 4 {
		t.Fatalf("retried while degraded: %d", n.Load())
	}

	mode.Store(int32(stubOK))
	s.Reset()
	waitState(t, states, Starting)
	waitState(t, states, Ready)
	if _, err := s.Client(); err != nil {
		t.Fatal(err)
	}
	stopAndCheckLeaks(t, s, base)
}

func TestResetReplacesHealthyProcess(t *testing.T) {
	var n atomic.Int32
	s := New(fastCfg(stubLauncher(stubOK, &n)))
	states, cancel := s.Subscribe()
	defer cancel()
	s.Start(context.Background())
	waitState(t, states, Ready)
	first, _ := s.Client()
	s.Reset()
	waitState(t, states, Starting)
	waitState(t, states, Ready)
	second, _ := s.Client()
	if first == second || n.Load() != 2 {
		t.Fatalf("not replaced, launches=%d", n.Load())
	}
	select {
	case <-first.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("old process still running")
	}
	_ = s.Stop(testCtx(t))
}

func TestPermanentLaunchErrorDegradesImmediately(t *testing.T) {
	var n atomic.Int32
	s := New(fastCfg(func(context.Context) (*stt.Client, error) {
		n.Add(1)
		return nil, ErrPermanent
	}))
	states, cancel := s.Subscribe()
	defer cancel()
	s.Start(context.Background())
	st := waitState(t, states, Degraded)
	if st.Reason != ReasonLaunchRefused || n.Load() != 1 {
		t.Fatalf("%+v launches=%d", st, n.Load())
	}
	_ = s.Stop(testCtx(t))
}

func TestStopDuringBackoffAndBeforeStart(t *testing.T) {
	base := runtime.NumGoroutine()
	cfg := fastCfg(func(context.Context) (*stt.Client, error) { return nil, errors.New("boom") })
	cfg.BackoffInitial, cfg.BackoffMax = time.Hour, time.Hour
	s := New(cfg)
	states, cancel := s.Subscribe()
	defer cancel()
	s.Start(context.Background())
	waitState(t, states, Restarting)
	stopAndCheckLeaks(t, s, base)
	if _, ok := <-states; ok {
		for range states {
		}
	}

	idle := New(fastCfg(nil))
	if err := idle.Stop(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	ch, _ := idle.Subscribe()
	if st := <-ch; st.Kind != Stopped {
		t.Fatalf("%+v", st)
	}
}
