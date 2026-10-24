//go:build live && linux

// The live dictation test runs the real chain on this machine: the
// compositor's "companion ctl dictate-toggle" path over the control
// socket, a recorded utterance instead of the microphone, the real STT
// sidecar and the real text insertion into a terminal it opens itself:
//
//	OPENCLICK_SIDECAR=bin/stt-sidecar OPENCLICK_WHISPER_MODEL=ggml-tiny.en.bin \
//	OPENCLICK_SAMPLE=third_party/whisper.cpp/samples/jfk.wav \
//	go test -tags=novulkan,live -run TestLiveDictation -v ./cmd/companion
//
// It needs a desktop session and kitty; focus must follow the new window.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/dictation"
	"github.com/xlsft/companion/internal/platform/control"
	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/internal/platform/insert"
	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/stt/protocol"
	"github.com/xlsft/companion/pkg/contracts"
)

func TestLiveDictation(t *testing.T) {
	sidecar, model, sample := os.Getenv("OPENCLICK_SIDECAR"), os.Getenv("OPENCLICK_WHISPER_MODEL"), os.Getenv("OPENCLICK_SAMPLE")
	if sidecar == "" || model == "" || sample == "" {
		t.Skip("OPENCLICK_SIDECAR, OPENCLICK_WHISPER_MODEL and OPENCLICK_SAMPLE not set")
	}
	pcm := readWAV(t, sample)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, _ := detect.New().Detect(ctx)

	client, err := stt.Start(ctx, stt.Config{Path: sidecar,
		Init: protocol.InitializeParams{ModelPath: model, Language: "en", Backend: protocol.BackendCPU}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())

	var logs bytes.Buffer
	var logMu sync.Mutex
	in := insert.New(report, insert.Options{TokenPath: filepath.Join(t.TempDir(), "token")})
	defer in.Close()
	svc := dictation.New(dictation.Config{
		Recorder: &dictation.MicRecorder{
			Mic: wavMic{pcm},
			STT: func(ctx context.Context, opts ...stt.SessionOption) (dictation.STTSession, error) {
				return client.StartSession(ctx, opts...)
			},
			Language: func() string { return "en" },
		},
		Inserter: in,
		Log:      slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &logMu}, nil)),
	})
	defer svc.Close()

	// The compositor's bind runs "companion ctl dictate-toggle".
	sock := filepath.Join(t.TempDir(), "control.sock")
	srv, err := control.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if _, err := srv.Register(ctx, []contracts.HotkeyBinding{{ID: control.DictateID}}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for ev := range srv.Events() {
			if ev.ID == control.DictateID {
				svc.Hotkey(ev.Kind)
			}
		}
	}()

	out := filepath.Join(t.TempDir(), "typed.txt")
	// Non-canonical: cat gets every character at once, no Enter needed.
	term := exec.Command("kitty", "--class", "companion-dictation-test", "sh", "-c", "stty -echo -icanon; cat > "+out)
	if err := term.Start(); err != nil {
		t.Skip("kitty:", err)
	}
	defer func() { _ = term.Process.Kill(); _ = term.Wait() }()
	if !focusedOn(ctx, "companion-dictation-test", 5*time.Second) {
		t.Skip("the test window did not get keyboard focus; not typing into another window")
	}
	if b, _ := os.ReadFile(out); len(b) > 0 {
		t.Logf("before dictating the terminal already got %q", b)
	}

	t0 := time.Now()
	if err := control.Ctl(ctx, sock, "dictate-toggle"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return svc.State().Phase == dictation.Listening })
	time.Sleep(500 * time.Millisecond) // the utterance reaches the recogniser
	if !focusedOn(ctx, "companion-dictation-test", time.Second) {
		svc.Cancel()
		t.Skip("the test window lost keyboard focus; not typing into another window")
	}
	if err := control.Ctl(ctx, sock, "dictate-toggle"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !svc.Active() })
	t.Logf("toggle to typed: %v", time.Since(t0))
	if st := svc.State(); st.Err != "" {
		t.Fatalf("dictation failed: %s", st.Err)
	}
	// Wait until the terminal has passed everything on.
	var got string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		b, _ := os.ReadFile(out)
		if string(b) == got && got != "" {
			break
		}
		got = string(b)
	}
	t.Logf("typed into the terminal: %q", got)
	if !strings.Contains(strings.ToLower(got), "ask not what your country can do for you") {
		t.Errorf("unexpected text %q", got)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if strings.Contains(strings.ToLower(logs.String()), "country") {
		t.Errorf("the transcript reached the log:\n%s", logs.String())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// readWAV reads a 16 kHz mono PCM16 WAV file.
func readWAV(t *testing.T, path string) []int16 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skip("sample: ", err)
	}
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:]))
		off += 8
		if id == "data" {
			return protocol.DecodePCM16(b[off:min(off+size, len(b))])
		}
		off += size + size%2
	}
	t.Fatal("no data chunk")
	return nil
}

// wavMic is a microphone that says the sample once.
type wavMic struct{ pcm []int16 }

func (wavMic) Devices(context.Context) ([]audio.Device, error) {
	return []audio.Device{{ID: "wav", Name: "sample", Default: true}}, nil
}

func (w wavMic) Open(ctx context.Context, _ string) (audio.Stream, error) {
	s := &wavStream{frames: make(chan audio.Frame, 8), done: make(chan struct{})}
	go func() {
		defer close(s.frames)
		const n = 320 // 20 ms
		for i := 0; i < len(w.pcm); i += n {
			f := audio.Frame{Samples: append([]int16(nil), w.pcm[i:min(i+n, len(w.pcm))]...)}
			select {
			case s.frames <- f:
			case <-s.done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return s, nil
}

type wavStream struct {
	frames chan audio.Frame
	done   chan struct{}
	once   sync.Once
}

func (s *wavStream) Frames() <-chan audio.Frame { return s.frames }
func (s *wavStream) Err() error                 { return nil }
func (s *wavStream) Level() audio.Level         { return audio.Level{} }
func (s *wavStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// focusedOn waits until the window with appID has keyboard focus, so a
// live test never types into someone else's window. Only niri can tell;
// elsewhere it trusts that a new window takes focus.
func focusedOn(ctx context.Context, appID string, wait time.Duration) bool {
	if os.Getenv("NIRI_SOCKET") == "" {
		time.Sleep(1500 * time.Millisecond)
		return true
	}
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		out, err := exec.CommandContext(ctx, "niri", "msg", "--json", "focused-window").Output()
		var w struct {
			AppID string `json:"app_id"`
		}
		if err == nil && json.Unmarshal(out, &w) == nil && w.AppID == appID {
			time.Sleep(300 * time.Millisecond) // let the client settle its keymap
			return true
		}
	}
	return false
}
