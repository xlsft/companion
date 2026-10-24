package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultFeed is the GitHub API base of the release repository.
const DefaultFeed = "https://api.github.com/repos/xlsft/companion"

// maxFeedBody bounds a feed response; a page of releases is a few hundred KB.
const maxFeedBody = 4 << 20

// timeNow is swapped in tests.
var timeNow = time.Now

// Checker asks the release feed for the newest release. It remembers each
// response's ETag, so a repeated check that finds nothing new is a 304 and
// does not count against GitHub's rate limit.
type Checker struct {
	Client *http.Client
	// Feed is the repository API base, DefaultFeed when empty; tests and
	// the dev hook COMPANION_UPDATE_URL point it at a local server.
	Feed string
	// Token is optional (GITHUB_TOKEN); it raises the rate limit and
	// reaches private repositories. Never logged.
	Token     string
	UserAgent string

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	etag string
	body []byte
}

func (c *Checker) feed() string {
	if c.Feed == "" {
		return DefaultFeed
	}
	return strings.TrimRight(c.Feed, "/")
}

// Latest is the newest non-draft release, pre-releases included only when
// pre is set. GitHub's /releases/latest already skips both, so the list
// endpoint is used only for pre-releases.
func (c *Checker) Latest(ctx context.Context, pre bool) (Release, error) {
	if !pre {
		body, err := c.get(ctx, c.feed()+"/releases/latest")
		if err != nil {
			return Release{}, err
		}
		var r Release
		if err := json.Unmarshal(body, &r); err != nil {
			return Release{}, fmt.Errorf("%w: %v", ErrFeed, err)
		}
		if r.Version, err = ParseVersion(r.Tag); err != nil || r.Draft || r.Prerelease || r.Version.Prerelease() {
			return Release{}, fmt.Errorf("%w: latest release is not a stable version", ErrFeed)
		}
		return r, nil
	}
	body, err := c.get(ctx, c.feed()+"/releases?per_page=30")
	if err != nil {
		return Release{}, err
	}
	var list []Release
	if err := json.Unmarshal(body, &list); err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrFeed, err)
	}
	var best Release
	found := false
	for _, r := range list {
		v, err := ParseVersion(r.Tag)
		if err != nil || r.Draft {
			continue // foreign tags and drafts are not releases of the app
		}
		r.Version = v
		if !found || Compare(v, best.Version) > 0 {
			best, found = r, true
		}
	}
	if !found {
		return Release{}, fmt.Errorf("%w: no releases", ErrUnavailable)
	}
	return best, nil
}

// Newer returns the latest release when it is newer than current.
func (c *Checker) Newer(ctx context.Context, current Version, pre bool) (Release, bool, error) {
	r, err := c.Latest(ctx, pre)
	if err != nil {
		return Release{}, false, err
	}
	return r, Compare(r.Version, current) > 0, nil
}

func (c *Checker) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	c.mu.Lock()
	prev, havePrev := c.cache[url]
	c.mu.Unlock()
	if havePrev {
		req.Header.Set("If-None-Match", prev.etag)
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrOffline, errors.Unwrap(err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && havePrev:
		return prev.body, nil
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBody))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOffline, err)
		}
		if etag := resp.Header.Get("ETag"); etag != "" {
			c.mu.Lock()
			if c.cache == nil {
				c.cache = map[string]cached{}
			}
			c.cache[url] = cached{etag: etag, body: body}
			c.mu.Unlock()
		}
		return body, nil
	}
	if rl := rateLimit(resp, timeNow()); rl != nil {
		return nil, rl
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	return nil, fmt.Errorf("%w: HTTP %d", ErrFeed, resp.StatusCode)
}

// rateLimit recognises GitHub's primary (X-RateLimit-Remaining: 0) and
// secondary (Retry-After) limits.
func rateLimit(resp *http.Response, now time.Time) *RateLimitError {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return &RateLimitError{Reset: now.Add(time.Duration(n) * time.Second)}
		}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		reset := now.Add(time.Hour)
		if n, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			reset = time.Unix(n, 0)
		}
		return &RateLimitError{Reset: reset}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RateLimitError{Reset: now.Add(time.Hour)}
	}
	return nil
}
