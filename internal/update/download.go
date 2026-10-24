package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Downloader fetches release assets.
type Downloader struct {
	Client    *http.Client
	Token     string // optional; see Checker.Token
	UserAgent string
}

// Progress reports bytes done of total; total is 0 when unknown.
type Progress func(done, total int64)

// Fetch downloads a into dst. Bytes land in dst+".part" first, so an
// interrupted download resumes with a Range request next time and a
// partial file is never taken for a complete one.
func (d *Downloader) Fetch(ctx context.Context, a Asset, dst string, progress Progress) error {
	part := dst + ".part"
	var have int64
	if st, err := os.Stat(part); err == nil && (a.Size == 0 || st.Size() < a.Size) {
		have = st.Size()
	} else if err == nil {
		// Larger than (or exactly) the asset but never renamed: start over
		// rather than trust it.
		os.Remove(part)
	}
	req, err := d.request(ctx, a)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrOffline, errors.Unwrap(err))
	}
	defer resp.Body.Close()
	flags := os.O_WRONLY | os.O_CREATE
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0 && strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes "+strconv.FormatInt(have, 10)+"-"):
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		// The server ignored the range: rewrite from the start.
		have = 0
		flags |= os.O_TRUNC
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part)
		return fmt.Errorf("%w: download range rejected", ErrFeed)
	default:
		if rl := rateLimit(resp, timeNow()); rl != nil {
			return rl
		}
		return fmt.Errorf("%w: download HTTP %d", ErrFeed, resp.StatusCode)
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return err
	}
	total := a.Size
	if total == 0 && resp.ContentLength > 0 {
		total = have + resp.ContentLength
	}
	w := &progressWriter{w: f, done: have, total: total, fn: progress}
	if progress != nil {
		progress(have, total)
	}
	src := io.Reader(resp.Body)
	if a.Size > 0 {
		// Never write past the advertised size, whatever the server sends.
		src = io.LimitReader(src, a.Size-have+1)
	}
	_, cerr := io.Copy(w, &ctxReader{ctx: ctx, r: src})
	if err := f.Sync(); cerr == nil {
		cerr = err
	}
	if err := f.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrOffline, cerr)
	}
	if a.Size > 0 && w.done != a.Size {
		if w.done > a.Size {
			os.Remove(part)
		}
		return fmt.Errorf("%w: got %d of %d bytes", ErrOffline, w.done, a.Size)
	}
	return os.Rename(part, dst)
}

// FetchBytes downloads a small asset (checksums, signature) into memory.
func (d *Downloader) FetchBytes(ctx context.Context, a Asset, limit int64) ([]byte, error) {
	req, err := d.request(ctx, a)
	if err != nil {
		return nil, err
	}
	client := d.Client
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
	if resp.StatusCode != http.StatusOK {
		if rl := rateLimit(resp, timeNow()); rl != nil {
			return nil, rl
		}
		return nil, fmt.Errorf("%w: download HTTP %d", ErrFeed, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOffline, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %s is too large", ErrFeed, a.Name)
	}
	return b, nil
}

// request uses the API URL with a token, since the public link of a
// private repository's asset does not accept one.
func (d *Downloader) request(ctx context.Context, a Asset) (*http.Request, error) {
	url := a.DownloadURL
	if d.Token != "" && a.APIURL != "" {
		url = a.APIURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
	}
	// net/http drops Authorization on a redirect to another host, so the
	// token never reaches the storage the asset redirects to.
	if d.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.Token)
	}
	return req, nil
}

type progressWriter struct {
	w           io.Writer
	done, total int64
	fn          Progress
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.fn != nil {
		p.fn(p.done, p.total)
	}
	return n, err
}

// ctxReader stops a copy promptly once ctx is done, even while the body
// keeps delivering bytes.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}
