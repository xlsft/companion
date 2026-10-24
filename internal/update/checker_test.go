package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

const latestJSON = `{"tag_name":"v0.2.0","html_url":"https://example.invalid/r/v0.2.0","draft":false,"prerelease":false,
	"assets":[{"name":"companion-0.2.0-linux-x86_64.tar.gz","size":3,"browser_download_url":"x","url":"y"}]}`

const listJSON = `[
	{"tag_name":"v0.4.0","draft":true},
	{"tag_name":"v0.3.0-rc.1","prerelease":true},
	{"tag_name":"nightly","prerelease":true},
	{"tag_name":"v0.2.0"}
]`

func TestCheckerResponses(t *testing.T) {
	reset := time.Unix(2000000000, 0)
	tests := []struct {
		name    string
		pre     bool
		handler http.HandlerFunc
		want    string // version, or "" on error
		err     error
		rate    bool
	}{
		{"latest", false, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/x/y/releases/latest" || r.Header.Get("Accept") != "application/vnd.github+json" {
				http.Error(w, "bad request", 400)
				return
			}
			w.Write([]byte(latestJSON))
		}, "0.2.0", nil, false},
		{"pre-releases skip drafts and foreign tags", true, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/x/y/releases" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(listJSON))
		}, "0.3.0-rc.1", nil, false},
		{"latest marked pre-release is rejected", false, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"tag_name":"v0.3.0-rc.1","prerelease":true}`))
		}, "", ErrFeed, false},
		{"no releases yet", false, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "", ErrUnavailable, false},
		{"private without token", false, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }, "", ErrUnavailable, false},
		{"forbidden", false, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }, "", ErrUnavailable, false},
		{"rate limited", false, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
		}, "", nil, true},
		{"secondary rate limit", false, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
		}, "", nil, true},
		{"server error", false, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }, "", ErrFeed, false},
		{"not json", false, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) }, "", ErrFeed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			c := &Checker{Client: srv.Client(), Feed: srv.URL + "/repos/x/y"}
			r, err := c.Latest(context.Background(), tt.pre)
			if tt.rate {
				var rl *RateLimitError
				if !errors.As(err, &rl) || Code(err) != "rate_limited" {
					t.Fatalf("err = %v, want rate limit", err)
				}
				if tt.name == "rate limited" && !rl.Reset.Equal(reset) {
					t.Errorf("reset = %v", rl.Reset)
				}
				return
			}
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Version.String() != tt.want {
				t.Errorf("version = %s, want %s", r.Version, tt.want)
			}
		})
	}
}

// A repeated check sends the ETag and reuses the body on 304, which
// GitHub does not count against the rate limit.
func TestCheckerETag(t *testing.T) {
	var full, notModified atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"abc"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("token not sent")
		}
		full.Add(1)
		w.Header().Set("ETag", `"abc"`)
		w.Write([]byte(latestJSON))
	}))
	defer srv.Close()
	c := &Checker{Client: srv.Client(), Feed: srv.URL, Token: "tok"}
	cur, _ := ParseVersion("0.0.1")
	for i := range 3 {
		r, newer, err := c.Newer(context.Background(), cur, false)
		if err != nil || !newer || r.Version.String() != "0.2.0" {
			t.Fatalf("check %d: %v %v %v", i, r.Version, newer, err)
		}
	}
	if full.Load() != 1 || notModified.Load() != 2 {
		t.Errorf("full=%d notModified=%d", full.Load(), notModified.Load())
	}
	// The same version is not newer.
	cur, _ = ParseVersion("0.2.0")
	if _, newer, err := c.Newer(context.Background(), cur, false); err != nil || newer {
		t.Errorf("newer=%v err=%v", newer, err)
	}
}

func TestCheckerOffline(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := &Checker{Feed: url}
	if _, err := c.Latest(context.Background(), false); !errors.Is(err, ErrOffline) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Latest(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: err = %v", err)
	}
}
