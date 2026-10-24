package tts

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testKey  = "sk-SECRET-KEY-123"
	testText = "TOPSECRET spoken words"
)

// conformance runs the behaviour every HTTP provider must share against an
// httptest server that only answers on streamPath.
type conformance struct {
	streamPath string
	// newProvider builds the provider under test against a server URL.
	newProvider func(baseURL string) Provider
}

func (c conformance) server(t *testing.T, h http.HandlerFunc) Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != c.streamPath {
			http.NotFound(w, r)
			return
		}
		// Net/http only watches for client disconnects (r.Context) once the
		// request body is consumed.
		io.Copy(io.Discard, r.Body)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return c.newProvider(srv.URL)
}

func req() SynthesisRequest {
	return SynthesisRequest{Text: testText, VoiceID: "voice1", Speed: 1}
}

func chunk(n int) []byte { return make([]byte, n) }

func (c conformance) run(t *testing.T) {
	t.Run("incremental delivery", func(t *testing.T) {
		gate := make(chan struct{})
		var finished atomic.Bool
		p := c.server(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write(chunk(960))
			w.(http.Flusher).Flush()
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			for i := 0; i < 3; i++ {
				w.Write(chunk(960))
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
			finished.Store(true)
		})
		s, err := p.SynthesizeStream(context.Background(), req())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		first, err := s.Next(context.Background())
		if err != nil || len(first.Data) == 0 {
			t.Fatalf("first chunk: %v %d", err, len(first.Data))
		}
		if finished.Load() {
			t.Fatal("server finished before first chunk was consumed: body was buffered")
		}
		if first.Format != PCM24kMono {
			t.Errorf("format = %v", first.Format)
		}
		close(gate)
		total := len(first.Data)
		for {
			ch, err := s.Next(context.Background())
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			total += len(ch.Data)
		}
		if total != 4*960 {
			t.Errorf("total = %d", total)
		}
	})

	for _, mode := range []string{"ctx", "close", "provider"} {
		t.Run("cancel mid-stream "+mode, func(t *testing.T) {
			gone := make(chan struct{})
			p := c.server(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				w.Write(chunk(960))
				w.(http.Flusher).Flush()
				<-r.Context().Done() // the server only unblocks if the client aborts
				close(gone)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, err := p.SynthesizeStream(ctx, req())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Next(context.Background()); err != nil {
				t.Fatal(err)
			}
			errc := make(chan error, 1)
			go func() { _, err := s.Next(context.Background()); errc <- err }()
			time.Sleep(20 * time.Millisecond)
			start := time.Now()
			switch mode {
			case "ctx":
				cancel()
			case "close":
				s.Close()
			case "provider":
				p.Cancel(context.Background())
			}
			select {
			case err := <-errc:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("err = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Next did not return after cancel")
			}
			if d := time.Since(start); d > 500*time.Millisecond {
				t.Errorf("cancel took %v", d)
			}
			select {
			case <-gone:
			case <-time.After(2 * time.Second):
				t.Error("server never saw the connection abort")
			}
			s.Close()
		})
	}

	t.Run("next ctx does not kill stream", func(t *testing.T) {
		release := make(chan struct{})
		p := c.server(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Write(chunk(960))
		})
		s, err := p.SynthesizeStream(context.Background(), req())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if _, err := s.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		close(release)
		if ch, err := s.Next(context.Background()); err != nil || len(ch.Data) != 960 {
			t.Errorf("after timeout: %v %d", err, len(ch.Data))
		}
	})

	statuses := []struct {
		status int
		retry  string
		kind   error
		after  time.Duration
	}{
		{401, "", ErrAuth, 0},
		{403, "", ErrAuth, 0},
		{429, "3", ErrRateLimited, 3 * time.Second},
		{500, "", ErrUnavailable, 0},
		{503, "", ErrUnavailable, 0},
		{400, "", ErrBadRequest, 0},
		{422, "", ErrBadRequest, 0},
	}
	for _, tt := range statuses {
		t.Run("status "+http.StatusText(tt.status), func(t *testing.T) {
			p := c.server(t, func(w http.ResponseWriter, r *http.Request) {
				if tt.retry != "" {
					w.Header().Set("Retry-After", tt.retry)
				}
				// Echo secrets in the body: they must never reach the error.
				http.Error(w, "bad "+testKey+" "+testText, tt.status)
			})
			_, err := p.SynthesizeStream(context.Background(), req())
			var e *Error
			if !errors.Is(err, tt.kind) || !errors.As(err, &e) {
				t.Fatalf("err = %v, want %v", err, tt.kind)
			}
			if e.Status != tt.status || e.RetryAfter != tt.after {
				t.Errorf("status=%d retryAfter=%v", e.Status, e.RetryAfter)
			}
			noSecrets(t, err)
		})
	}

	t.Run("connection dropped mid-stream", func(t *testing.T) {
		p := c.server(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100000") // promise more than we send
			w.WriteHeader(200)
			w.Write(chunk(960))
			w.(http.Flusher).Flush()
		})
		s, err := p.SynthesizeStream(context.Background(), req())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var last error
		for i := 0; i < 20 && last == nil; i++ {
			_, last = s.Next(context.Background())
		}
		if last == nil || last == io.EOF {
			t.Fatalf("want error, got %v", last)
		}
		if !errors.Is(last, ErrUnavailable) {
			t.Errorf("err = %v", last)
		}
		noSecrets(t, last)
		if _, again := s.Next(context.Background()); again != last {
			t.Errorf("error not sticky: %v", again)
		}
	})

	t.Run("server down", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		p := c.newProvider(srv.URL)
		srv.Close()
		_, err := p.SynthesizeStream(context.Background(), req())
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
		noSecrets(t, err)
	})

	t.Run("header timeout", func(t *testing.T) {
		stall := make(chan struct{})
		t.Cleanup(func() { close(stall) })
		p := c.server(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-stall:
			case <-r.Context().Done():
			}
		})
		// Rebuild with a tiny timeout via the per-provider hook.
		p = c.withTimeout(t, p, 50*time.Millisecond)
		start := time.Now()
		_, err := p.SynthesizeStream(context.Background(), req())
		if !errors.Is(err, ErrUnavailable) || time.Since(start) > time.Second {
			t.Errorf("err=%v after %v", err, time.Since(start))
		}
	})

	t.Run("speed validation", func(t *testing.T) {
		var hits atomic.Int32
		p := c.server(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
		for _, sp := range []float64{0.49, 1.51, -1} {
			r := req()
			r.Speed = sp
			if _, err := p.SynthesizeStream(context.Background(), r); !errors.Is(err, ErrBadRequest) {
				t.Errorf("speed %v: err = %v", sp, err)
			}
		}
		if hits.Load() != 0 {
			t.Error("invalid request reached the server")
		}
	})
}

// withTimeout is wired by the per-provider test through timeoutProvider.
func (c conformance) withTimeout(t *testing.T, p Provider, d time.Duration) Provider {
	t.Helper()
	type timeouter interface{ setTimeout(time.Duration) }
	tp, ok := p.(timeouter)
	if !ok {
		t.Fatal("provider does not expose setTimeout")
	}
	tp.setTimeout(d)
	return p
}

// Test-only knob so the header-timeout case needs no sleeping on the default.
func (b *base) setTimeout(d time.Duration) { b.cfg.Timeout = d }

func noSecrets(t *testing.T, err error) {
	t.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		if m := e.Error(); strings.Contains(m, testKey) || strings.Contains(m, "TOPSECRET") {
			t.Errorf("error leaks secret: %q", m)
		}
	}
}
