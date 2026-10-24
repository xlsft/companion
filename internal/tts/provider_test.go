package tts

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	ok := SynthesisRequest{Text: "hi", VoiceID: "v"}
	tests := []struct {
		name    string
		mut     func(*SynthesisRequest)
		wantErr bool
	}{
		{"defaults", func(*SynthesisRequest) {}, false},
		{"min speed", func(r *SynthesisRequest) { r.Speed = 0.5 }, false},
		{"max speed", func(r *SynthesisRequest) { r.Speed = 1.5 }, false},
		{"too slow", func(r *SynthesisRequest) { r.Speed = 0.49 }, true},
		{"too fast", func(r *SynthesisRequest) { r.Speed = 1.51 }, true},
		{"negative", func(r *SynthesisRequest) { r.Speed = -1 }, true},
		{"empty text", func(r *SynthesisRequest) { r.Text = "" }, true},
		{"empty voice", func(r *SynthesisRequest) { r.VoiceID = "" }, true},
		{"pcm without rate", func(r *SynthesisRequest) { r.Format = Format{Container: ContainerPCM16} }, true},
		{"unknown container", func(r *SynthesisRequest) { r.Format = Format{Container: "ogg"} }, true},
		{"mp3", func(r *SynthesisRequest) { r.Format = MP3 }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ok
			tt.mut(&r)
			got, err := r.normalize()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrBadRequest) {
					t.Errorf("want ErrBadRequest, got %v", err)
				}
				return
			}
			if got.Speed == 0 || got.Format == (Format{}) {
				t.Errorf("defaults not applied: %+v", got)
			}
		})
	}
}

func TestNormalizeErrorOmitsText(t *testing.T) {
	_, err := SynthesisRequest{Text: "top secret words", VoiceID: "v", Speed: 9}.normalize()
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusError(t *testing.T) {
	tests := []struct {
		status int
		retry  string
		kind   error
		after  time.Duration
	}{
		{401, "", ErrAuth, 0},
		{403, "", ErrAuth, 0},
		{429, "7", ErrRateLimited, 7 * time.Second},
		{429, "", ErrRateLimited, 0},
		{429, "garbage", ErrRateLimited, 0},
		{500, "", ErrUnavailable, 0},
		{503, "", ErrUnavailable, 0},
		{400, "", ErrBadRequest, 0},
		{422, "", ErrBadRequest, 0},
	}
	for _, tt := range tests {
		resp := &http.Response{StatusCode: tt.status, Header: http.Header{}}
		if tt.retry != "" {
			resp.Header.Set("Retry-After", tt.retry)
		}
		err := statusError("p", resp)
		var e *Error
		if !errors.As(err, &e) || !errors.Is(err, tt.kind) || e.Status != tt.status || e.RetryAfter != tt.after {
			t.Errorf("status %d retry %q: %+v", tt.status, tt.retry, err)
		}
	}
}

func TestRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now)
	if got != 90*time.Second {
		t.Errorf("got %v", got)
	}
}

func drain(t *testing.T, s Stream) (n int, err error) {
	t.Helper()
	for {
		c, err := s.Next(context.Background())
		if err != nil {
			return n, err
		}
		n += len(c.Data)
	}
}

func TestFakeStream(t *testing.T) {
	f := &Fake{Chunks: 3, ChunkBytes: 960}
	s, err := f.SynthesizeStream(context.Background(), SynthesisRequest{Text: "x", VoiceID: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, err := drain(t, s)
	if err != io.EOF || n != 3*960 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := s.Next(context.Background()); err != io.EOF {
		t.Errorf("EOF should be sticky, got %v", err)
	}
	if len(f.Requests()) != 1 {
		t.Errorf("requests not recorded")
	}
}

func TestFakeDeterministic(t *testing.T) {
	read := func() []byte {
		f := &Fake{Chunks: 2, ChunkBytes: 480}
		s, _ := f.SynthesizeStream(context.Background(), SynthesisRequest{Text: "x", VoiceID: "v"})
		defer s.Close()
		var all []byte
		for {
			c, err := s.Next(context.Background())
			if err != nil {
				return all
			}
			all = append(all, c.Data...)
		}
	}
	a, b := read(), read()
	if len(a) != 960 || string(a) != string(b) {
		t.Fatalf("not deterministic: %d %d", len(a), len(b))
	}
}

func TestFakeCloseAndCancel(t *testing.T) {
	f := &Fake{Chunks: 100, Delay: 50 * time.Millisecond}
	s, err := f.SynthesizeStream(context.Background(), SynthesisRequest{Text: "x", VoiceID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f.Cancel(context.Background())
	_, err = s.Next(context.Background())
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Errorf("err=%v after %v", err, time.Since(start))
	}
	s.Close()
	s.Close()
}

func TestNextContext(t *testing.T) {
	f := &Fake{Chunks: 2, Delay: time.Second}
	s, _ := f.SynthesizeStream(context.Background(), SynthesisRequest{Text: "x", VoiceID: "v"})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestFakeRejectsBadSpeed(t *testing.T) {
	_, err := (&Fake{}).SynthesizeStream(context.Background(), SynthesisRequest{Text: "x", VoiceID: "v", Speed: 3})
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("err = %v", err)
	}
}
