package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xlsft/companion/internal/models/disk"
)

var (
	// ErrChecksum means the downloaded bytes do not match the manifest; the
	// partial file has been deleted and the model is not installed.
	ErrChecksum = errors.New("model checksum mismatch")
	// ErrInsufficientSpace means the target volume cannot hold the remaining bytes plus margin.
	ErrInsufficientSpace = errors.New("insufficient disk space")
)

const (
	partSuffix = ".part"
	okSuffix   = ".ok"

	progressInterval = 100 * time.Millisecond // ~10 updates/s
	bufSize          = 256 << 10
)

// Progress is a throttled snapshot of a running download.
type Progress struct {
	Done  int64
	Total int64
	// Rate is smoothed bytes per second for this session; resumed bytes do not count.
	Rate float64
	// ETA is zero while the rate is unknown.
	ETA time.Duration
	// Unpacking is set once an archive is downloaded and verified; Done
	// and Total then count unpacked bytes and Rate/ETA are zero.
	Unpacking bool
}

// Downloader fetches models into a directory with resume and verification.
// The zero value is usable.
type Downloader struct {
	// Client defaults to http.DefaultClient.
	Client *http.Client
	// FreeSpace reports free bytes for a directory; defaults to the OS probe.
	// If it fails the check is skipped rather than blocking the download.
	FreeSpace func(dir string) (uint64, error)

	now func() time.Time // test hook
}

func (d *Downloader) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return http.DefaultClient
}

func (d *Downloader) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// Download makes the model available in destDir and returns the final path.
// Until the SHA-256 matches, only <file>.part exists; an already installed
// model is returned without network access. Cancelling ctx leaves the .part
// so a later call resumes.
func (d *Downloader) Download(ctx context.Context, m Manifest, destDir string, progress func(Progress)) (string, error) {
	if err := m.Validate(); err != nil {
		return "", fmt.Errorf("invalid manifest: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	if m.Archive() {
		return d.downloadArchive(ctx, m, destDir, progress)
	}
	final := filepath.Join(destDir, m.File)
	part := final + partSuffix

	if ok, err := adoptExisting(ctx, m, final); err != nil {
		return "", err
	} else if ok {
		emit(progress, Progress{Done: m.Size, Total: m.Size})
		return final, nil
	}

	// From here the model is not installed; make sure no stale record says otherwise.
	_ = os.Remove(final + okSuffix)

	h := sha256.New()
	have, err := hashExisting(ctx, part, h, m.Size)
	if err != nil {
		return "", err
	}

	if have < m.Size {
		need := m.Size - have
		if err := d.checkSpace(destDir, need); err != nil {
			return "", err
		}
	}

	t := newTracker(d.clock, have, m.Size, progress)
	t.force(have)

	if have < m.Size {
		have, err = d.fetch(ctx, m, part, h, have, t)
		if err != nil {
			return "", err
		}
	}
	t.force(have)

	if got := hex.EncodeToString(h.Sum(nil)); got != m.SHA256 {
		_ = os.Remove(part)
		return "", fmt.Errorf("%w: %s", ErrChecksum, m.ID)
	}
	if err := syncFile(part); err != nil {
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	syncDir(destDir)
	if err := writeRecord(final, m); err != nil {
		return "", err
	}
	return final, nil
}

func (d *Downloader) checkSpace(dir string, need int64) error {
	free := d.FreeSpace
	if free == nil {
		free = disk.Free
	}
	avail, err := free(dir)
	if err != nil {
		return nil
	}
	required := uint64(need) + uint64(need)/10
	if avail < required {
		return fmt.Errorf("%w: need %d bytes, have %d", ErrInsufficientSpace, required, avail)
	}
	return nil
}

// fetch streams the remainder of the file into part, appending to the
// existing hash state. It returns the total bytes now on disk.
func (d *Downloader) fetch(ctx context.Context, m Manifest, part string, h hash.Hash, have int64, t *tracker) (int64, error) {
	for attempt := 0; ; attempt++ {
		resp, err := d.request(ctx, m.URL, have)
		if err != nil {
			return have, err
		}
		restart := false
		switch resp.StatusCode {
		case http.StatusPartialContent:
			if !rangeMatches(resp.Header.Get("Content-Range"), have, m.Size) {
				restart = true
			}
		case http.StatusOK:
			if resp.ContentLength >= 0 && resp.ContentLength != m.Size {
				resp.Body.Close()
				return have, fmt.Errorf("server size %d differs from manifest %d", resp.ContentLength, m.Size)
			}
			// Server ignored Range (or this was a fresh start): body is the whole file.
			restart = have > 0
		case http.StatusRequestedRangeNotSatisfiable:
			restart = true
		default:
			resp.Body.Close()
			return have, fmt.Errorf("download %s: http status %d", m.ID, resp.StatusCode)
		}

		if restart && resp.StatusCode != http.StatusOK {
			// Unusable resume response; drop state and ask for the whole file.
			resp.Body.Close()
			if attempt > 0 {
				return have, errors.New("server resume response inconsistent")
			}
			have = 0
			h.Reset()
			t.reset(0)
			if err := os.Truncate(part, 0); err != nil && !os.IsNotExist(err) {
				return have, err
			}
			continue
		}

		flags := os.O_CREATE | os.O_WRONLY
		if restart {
			flags |= os.O_TRUNC
			have = 0
			h.Reset()
			t.reset(0)
		} else {
			flags |= os.O_APPEND
		}
		f, err := os.OpenFile(part, flags, 0o644)
		if err != nil {
			resp.Body.Close()
			return have, err
		}
		n, err := stream(ctx, resp.Body, f, h, m.Size-have, t, have)
		resp.Body.Close()
		cerr := f.Close()
		have += n
		if err != nil {
			return have, err
		}
		if cerr != nil {
			return have, cerr
		}
		if have != m.Size {
			return have, io.ErrUnexpectedEOF
		}
		return have, nil
	}
}

func (d *Downloader) request(ctx context.Context, url string, from int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if from > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-")
	}
	return d.client().Do(req)
}

// rangeMatches accepts "bytes <from>-<end>/<total>" that continues exactly where we stopped.
func rangeMatches(cr string, from, total int64) bool {
	rest, ok := strings.CutPrefix(cr, "bytes ")
	if !ok {
		return false
	}
	span, tot, ok := strings.Cut(rest, "/")
	if !ok {
		return false
	}
	start, end, ok := strings.Cut(span, "-")
	if !ok {
		return false
	}
	s, err1 := strconv.ParseInt(start, 10, 64)
	e, err2 := strconv.ParseInt(end, 10, 64)
	if err1 != nil || err2 != nil || s != from || e != total-1 {
		return false
	}
	if tot != "*" {
		if tt, err := strconv.ParseInt(tot, 10, 64); err != nil || tt != total {
			return false
		}
	}
	return true
}

// stream copies at most want bytes (plus one to detect overlong bodies).
func stream(ctx context.Context, r io.Reader, w io.Writer, h hash.Hash, want int64, t *tracker, base int64) (int64, error) {
	buf := make([]byte, bufSize)
	lr := io.LimitReader(r, want+1)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		k, rerr := lr.Read(buf)
		if k > 0 {
			if n+int64(k) > want {
				return n, errors.New("server sent more data than the manifest size")
			}
			if _, err := w.Write(buf[:k]); err != nil {
				return n, err
			}
			h.Write(buf[:k])
			n += int64(k)
			t.update(base + n)
		}
		if rerr == io.EOF {
			return n, nil
		}
		if rerr != nil {
			if cerr := ctx.Err(); cerr != nil {
				return n, cerr
			}
			return n, rerr
		}
	}
}

// hashExisting feeds an existing part file into h and returns its length. A
// part longer than the model is garbage and is removed.
func hashExisting(ctx context.Context, part string, h hash.Hash, size int64) (int64, error) {
	f, err := os.Open(part)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size() > size {
		f.Close()
		return 0, os.Remove(part)
	}
	buf := make([]byte, bufSize)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		k, rerr := f.Read(buf)
		h.Write(buf[:k])
		n += int64(k)
		if rerr == io.EOF {
			return n, nil
		}
		if rerr != nil {
			return 0, rerr
		}
	}
}

// adoptExisting handles a final file already on disk: trusted if its record
// matches, re-verified if the record is missing (crash between rename and
// record), deleted if it is wrong.
func adoptExisting(ctx context.Context, m Manifest, final string) (bool, error) {
	st, err := os.Stat(final)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if st.Size() == m.Size {
		if rec, err := readRecord(final + okSuffix); err == nil && rec.SHA256 == m.SHA256 && rec.Size == m.Size {
			return true, nil
		}
		h := sha256.New()
		if _, err := hashExisting(ctx, final, h, m.Size); err != nil {
			return false, err
		}
		if hex.EncodeToString(h.Sum(nil)) == m.SHA256 {
			return true, writeRecord(final, m)
		}
	}
	return false, os.Remove(final)
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir makes the rename durable where the OS allows opening directories.
func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		f.Close()
	}
}

func emit(fn func(Progress), p Progress) {
	if fn != nil {
		fn(p)
	}
}

// tracker throttles progress callbacks and smooths the transfer rate.
type tracker struct {
	now      func() time.Time
	fn       func(Progress)
	total    int64
	last     time.Time
	lastDone int64
	rate     float64
}

func newTracker(now func() time.Time, have, total int64, fn func(Progress)) *tracker {
	t := &tracker{now: now, fn: fn, total: total}
	t.reset(have)
	return t
}

func (t *tracker) reset(have int64) {
	t.lastDone, t.rate = have, 0
	t.last = t.now()
}

func (t *tracker) update(done int64) {
	if t.fn == nil || t.now().Sub(t.last) < progressInterval {
		return
	}
	t.force(done)
}

func (t *tracker) force(done int64) {
	if t.fn == nil {
		return
	}
	now := t.now()
	if dt := now.Sub(t.last).Seconds(); dt > 0 && done > t.lastDone {
		inst := float64(done-t.lastDone) / dt
		if t.rate == 0 {
			t.rate = inst
		} else {
			t.rate = 0.7*t.rate + 0.3*inst
		}
		t.last, t.lastDone = now, done
	}
	p := Progress{Done: done, Total: t.total, Rate: t.rate}
	if t.rate > 0 && done < t.total {
		p.ETA = time.Duration(float64(t.total-done) / t.rate * float64(time.Second))
	}
	t.fn(p)
}
