package stt

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/stt/engine/fake"
	"github.com/xlsft/companion/internal/stt/protocol"
	"github.com/xlsft/companion/internal/stt/server"
)

// sidecarBin is the real cmd/stt-sidecar built once for the process tests.
var sidecarBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stt-sidecar-test")
	if err == nil {
		bin := filepath.Join(dir, "stt-sidecar")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		build := exec.Command("go", "build", "-tags", sidecarTags, "-o", bin, "github.com/xlsft/companion/cmd/stt-sidecar")
		build.Stderr = os.Stderr
		if build.Run() == nil {
			sidecarBin = bin
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func needBinary(t *testing.T) {
	t.Helper()
	if sidecarBin == "" {
		t.Skip("could not build cmd/stt-sidecar")
	}
}

func inProcess(t *testing.T, opts fake.Options) *Client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := server.New(server.Config{
		EngineName:      "fake",
		Factory:         fake.Factory(opts),
		PartialInterval: 200 * time.Millisecond,
	})
	served := make(chan struct{})
	go func() {
		_ = srv.Serve(context.Background(), inR, outW)
		outW.Close()
		close(served)
	}()
	c := NewClient(outR, inW, nil)
	ctx := testCtx(t)
	if _, err := c.Initialize(ctx, protocol.InitializeParams{Language: "auto", Backend: "cpu"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Close(ctx)
		<-served
	})
	return c
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func tone(d time.Duration) []int16 {
	out := make([]int16, int(d.Seconds()*protocol.SampleRate))
	for i := range out {
		out[i] = int16(8000 * math.Sin(2*math.Pi*220*float64(i)/protocol.SampleRate))
	}
	return out
}

// writePaced writes 100 ms chunks with a small pause so rolling partials
// have a chance to run between chunks.
func writePaced(t *testing.T, s *Session, pcm []int16) {
	t.Helper()
	for i := 0; i < len(pcm); i += 1600 {
		if err := s.Write(pcm[i:min(i+1600, len(pcm))]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

func collect(s *Session) <-chan []Partial {
	out := make(chan []Partial, 1)
	go func() {
		var ps []Partial
		for p := range s.Partials() {
			ps = append(ps, p)
		}
		out <- ps
	}()
	return out
}

func runSession(t *testing.T, c *Client) {
	t.Helper()
	ctx := testCtx(t)
	s, err := c.StartSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	partials := collect(s)
	pcm := tone(2500 * time.Millisecond)
	writePaced(t, s, pcm)
	final, err := s.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := fake.Text(false, len(pcm)); final.Text != want || final.Duration != 2500*time.Millisecond || !final.Speech || final.Language != "en" {
		t.Fatalf("final %+v, want %q", final, want)
	}
	ps := <-partials // closed by Finish
	if len(ps) == 0 {
		t.Fatal("no partials")
	}
	for _, p := range ps {
		if p.Stable || p.Text == "" {
			t.Fatalf("partial %+v", p)
		}
	}
	if err := s.Write(pcm[:10]); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("write after finish: %v", err)
	}
}

func TestEndToEndInProcess(t *testing.T) {
	c := inProcess(t, fake.Options{})
	runSession(t, c)
	// The client is reusable across sessions.
	runSession(t, c)
}

func TestCancelDropsLateMessages(t *testing.T) {
	c := inProcess(t, fake.Options{Delay: 300 * time.Millisecond})
	ctx := testCtx(t)
	s, err := c.StartSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(tone(time.Second)); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := s.Finish(ctx)
		finished <- err
	}()
	time.Sleep(50 * time.Millisecond) // partial and final decode in flight
	s.Cancel()
	s.Cancel() // idempotent
	select {
	case err := <-finished:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("finish: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Finish did not return after Cancel")
	}
	// The in-flight partial completes after cancel; it must not surface.
	time.Sleep(400 * time.Millisecond)
	if _, ok := <-s.Partials(); ok {
		t.Fatal("partial delivered after cancel")
	}
	if err := s.Write(tone(10 * time.Millisecond)); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("write after cancel: %v", err)
	}
	if h, err := c.Health(ctx); err != nil || h.Sessions != 0 {
		t.Fatalf("health %+v %v", h, err)
	}
}

func TestFinishContextDeadline(t *testing.T) {
	c := inProcess(t, fake.Options{Delay: 5 * time.Second})
	s, err := c.StartSession(testCtx(t), WithPartials(false), WithLanguage("ru"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(tone(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.Finish(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("finish: %v", err)
	}
	// The sidecar must have been told to cancel, freeing the session.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h, err := c.Health(testCtx(t))
		if err != nil {
			t.Fatal(err)
		}
		if h.Sessions == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session not cancelled in sidecar")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSpawnedSidecar(t *testing.T) {
	needBinary(t)
	ctx := testCtx(t)
	c, err := Start(ctx, Config{
		Path: sidecarBin,
		Args: []string{"-engine=fake", "-partial-interval=200ms"},
		Init: protocol.InitializeParams{Language: "auto", Backend: "auto"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info := c.Info(); info.Engine != "fake" || info.ProtocolVersion != protocol.Version {
		t.Fatalf("info %+v", info)
	}
	runSession(t, c)
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(c.Err(), ErrSidecarExited) {
		t.Fatalf("after close: %v", c.Err())
	}
	if _, err := c.Health(ctx); !errors.Is(err, ErrSidecarExited) {
		t.Fatalf("health after close: %v", err)
	}
}

func TestSidecarCrashFailsPendingCalls(t *testing.T) {
	needBinary(t)
	ctx := testCtx(t)
	c, err := Start(ctx, Config{
		Path: sidecarBin,
		Args: []string{"-engine=fake", "-fake-exit-on-final"},
		Init: protocol.InitializeParams{Language: "en"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.StartSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(tone(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = s.Finish(ctx)
	if !errors.Is(err, ErrSidecarExited) {
		t.Fatalf("finish: %v", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(c.Err(), &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("exit status not surfaced: %v", c.Err())
	}
	if _, ok := <-s.Partials(); ok {
		t.Fatal("partials not closed")
	}
	if _, err := c.StartSession(ctx); !errors.Is(err, ErrSidecarExited) {
		t.Fatalf("start after crash: %v", err)
	}
}

func TestStartReportsInitError(t *testing.T) {
	needBinary(t)
	_, err := Start(testCtx(t), Config{
		Path: sidecarBin,
		Args: []string{"-engine=fake"},
		Init: protocol.InitializeParams{ModelPath: filepath.Join(t.TempDir(), "missing.bin")},
	})
	if !errors.Is(err, protocol.ErrModelNotFound) {
		t.Fatalf("got %v", err)
	}
}
