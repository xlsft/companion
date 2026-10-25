package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestIsSensitiveKey(t *testing.T) {
	for k, want := range map[string]bool{
		"api_key": true, "API_KEY": true, "apiKey": true, "x-api-key": true, "key": true,
		"token": true, "access_token": true, "Authorization": true, "password": true,
		"secret": true, "prompt": true, "transcript": true, "text": true, "content": true,
		"response": true, "image": true, "audio": true, "screenshot_image": true,
		"keyboard": false, "tokens_used": false, "component": false, "code": false,
		"latency_ms": false, "hotkey": false, "model": false,
	} {
		if got := IsSensitiveKey(k); got != want {
			t.Errorf("IsSensitiveKey(%q)=%v want %v", k, got, want)
		}
	}
}

func TestRedactString(t *testing.T) {
	b64 := "dGhpcyBpcyBhIHZlcnkgbG9uZyBiYXNlNjQgYmxvYjEyMzQ1Ng=="
	hex := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcDEF123_-xyz"
	tests := []struct {
		name, in string
		redact   bool
		keep     string // substring that must survive
	}{
		{"bearer", "request failed: Authorization: Bearer abc123DEF456ghi", true, "request failed"},
		{"sk", "bad key sk-proj1234567890abcdef", true, "bad key"},
		{"sk-ant", "bad key sk-ant-api03-AbCdEf123456", true, "bad key"},
		{"xi", "elevenlabs xi-abcdefghijkl1234", true, "elevenlabs"},
		{"jwt", "token " + jwt + " rejected", true, "rejected"},
		{"base64", "blob " + b64, true, "blob"},
		{"hex", "digest " + hex, true, "digest"},
		{"kv", "config api_key=supersecretvalue1 loaded", true, "api_key="},
		{"kv json", `{"password":"hunter2hunter2"}`, true, "password"},
		{"keyboard", "keyboard layout changed", false, ""},
		{"task", "task-force mask-ed disk-usage", false, ""},
		{"path", "open /home/user/.local/share/companion/models/ggml-base.bin", false, ""},
		{"kebab", "a-very-long-kebab-case-identifier-without-digits", false, ""},
		{"short token word", "token: expired", false, ""},
		{"plain", "stt sidecar exited code=137", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactString(tc.in)
			if tc.redact {
				if got == tc.in || !strings.Contains(got, Masked) {
					t.Fatalf("not redacted: %q", got)
				}
				for _, s := range []string{b64, hex, jwt, "abc123DEF456ghi", "supersecretvalue1", "hunter2hunter2", "AbCdEf123456", "abcdefghijkl1234", "proj1234567890"} {
					if strings.Contains(got, s) {
						t.Fatalf("secret %q survived: %q", s, got)
					}
				}
				if !strings.Contains(got, tc.keep) {
					t.Fatalf("lost context %q: %q", tc.keep, got)
				}
				if ContainsSecret(got) {
					t.Fatalf("not idempotent: %q", got)
				}
			} else if got != tc.in {
				t.Fatalf("false positive: %q -> %q", tc.in, got)
			}
		})
	}
}

func newTestLogger() (*slog.Logger, *bytes.Buffer, *ErrorLog) {
	var buf bytes.Buffer
	errs := NewErrorLog(3)
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(NewRedactingHandler(inner, WithErrorLog(errs))), &buf, errs
}

func TestHandlerRedactsAttrs(t *testing.T) {
	l, buf, _ := newTestLogger()
	l = l.With("API_KEY", "plainvalue")
	l.Info("using sk-ant-api03-AbCdEf123456",
		"prompt", "what is on my screen",
		slog.Group("req", slog.String("Authorization", "xyz"), slog.Group("inner", slog.String("transcript", "hello there")), slog.String("model", "gpt")),
		"err", errors.New("boom Bearer abcdefgh12345678"),
		"layout", "keyboard",
		"audio", []byte{1, 2, 3},
	)
	out := buf.String()
	for _, bad := range []string{"plainvalue", "AbCdEf123456", "what is on my screen", "xyz", "hello there", "abcdefgh12345678", "[1 2 3]"} {
		if strings.Contains(out, bad) {
			t.Errorf("leaked %q in %s", bad, out)
		}
	}
	for _, good := range []string{"model=gpt", "keyboard", "boom", Masked} {
		if !strings.Contains(out, good) {
			t.Errorf("missing %q in %s", good, out)
		}
	}
}

func TestErrorLogFeed(t *testing.T) {
	l, _, errs := newTestLogger()
	l.Info("ignored")
	l.Warn("w1", "component", "stt", "code", "STT_CRASH", "text", "secret words")
	l.With("component", "llm").Error("e1 sk-abcdefghijklmnop")
	snap := errs.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("got %d events", len(snap))
	}
	if snap[0].Component != "stt" || snap[0].Code != "STT_CRASH" || snap[0].Level != "WARN" {
		t.Errorf("event0 %+v", snap[0])
	}
	if snap[1].Component != "llm" || strings.Contains(snap[1].Message, "sk-abc") {
		t.Errorf("event1 %+v", snap[1])
	}
}

func TestErrorLogOnlyHandlerEnabled(t *testing.T) {
	errs := NewErrorLog(2)
	h := NewRedactingHandler(nil, WithErrorLog(errs))
	if h.Enabled(context.Background(), slog.LevelInfo) || !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("enabled levels wrong")
	}
	slog.New(h).Error("x")
	if len(errs.Snapshot()) != 1 {
		t.Fatal("not recorded")
	}
}

func TestRingBuffer(t *testing.T) {
	for _, tc := range []struct {
		n, add int
		want   []string
	}{
		{3, 0, nil},
		{3, 2, []string{"0", "1"}},
		{3, 3, []string{"0", "1", "2"}},
		{3, 5, []string{"2", "3", "4"}},
	} {
		l := NewErrorLog(tc.n)
		for i := 0; i < tc.add; i++ {
			l.Add(ErrorEvent{Message: string(rune('0' + i%10))})
		}
		snap := l.Snapshot()
		if len(snap) != len(tc.want) {
			t.Fatalf("n=%d add=%d: %v", tc.n, tc.add, snap)
		}
		for i, w := range tc.want {
			if snap[i].Message != w {
				t.Errorf("n=%d add=%d idx %d: %q want %q", tc.n, tc.add, i, snap[i].Message, w)
			}
		}
	}
	l := NewErrorLog(0)
	for i := 0; i < 250; i++ {
		l.Add(ErrorEvent{})
	}
	if got := len(l.Snapshot()); got != DefaultErrorLogSize {
		t.Fatalf("default size: %d", got)
	}
}

func TestErrorLogAddRedacts(t *testing.T) {
	l := NewErrorLog(1)
	l.Add(ErrorEvent{Message: "Bearer abcdefgh12345678"})
	if strings.Contains(l.Snapshot()[0].Message, "abcdefgh") {
		t.Fatal("leak")
	}
}
