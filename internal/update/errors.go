package update

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

var (
	// ErrUnavailable: the release feed answered 401/403/404, e.g. the
	// repository is private or has no releases yet. Not an alarm.
	ErrUnavailable = errors.New("update: release feed unavailable")
	// ErrOffline: the feed could not be reached at all.
	ErrOffline = errors.New("update: release feed unreachable")
	// ErrFeed: the feed answered something unexpected.
	ErrFeed = errors.New("update: unexpected release feed response")

	ErrNoAsset          = errors.New("update: release has no build for this system")
	ErrNoChecksums      = errors.New("update: release has no checksums")
	ErrNoSignature      = errors.New("update: release checksums are not signed")
	ErrBadSignature     = errors.New("update: release signature is not valid")
	ErrChecksumMissing  = errors.New("update: file is not listed in the checksums")
	ErrChecksumMismatch = errors.New("update: file does not match its checksum")
	ErrBadArchive       = errors.New("update: release archive is malformed")

	// ErrPackageManaged: the binaries belong to a system package; the
	// package manager updates them, not the app.
	ErrPackageManaged = errors.New("update: installed by a package manager")
	// ErrNotWritable: the install directory cannot be written.
	ErrNotWritable = errors.New("update: install directory is not writable")
)

// RateLimitError is a 403/429 from the feed with the quota spent.
type RateLimitError struct{ Reset time.Time }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("update: release feed rate limited until %s", e.Reset.UTC().Format(time.RFC3339))
}

// Code is a short technical code for err, safe to log and to map to UI
// text; it never carries URLs, tokens or paths.
func Code(err error) string {
	var rl *RateLimitError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &rl):
		return "rate_limited"
	case errors.Is(err, contracts.ErrUnsupported):
		return "unsupported"
	}
	for _, c := range []struct {
		err  error
		code string
	}{
		{ErrUnavailable, "unavailable"},
		{ErrOffline, "offline"},
		{ErrFeed, "feed"},
		{ErrNoAsset, "no_asset"},
		{ErrNoChecksums, "no_checksums"},
		{ErrNoSignature, "no_signature"},
		{ErrBadSignature, "bad_signature"},
		{ErrChecksumMissing, "checksum_missing"},
		{ErrChecksumMismatch, "checksum_mismatch"},
		{ErrBadArchive, "bad_archive"},
		{ErrPackageManaged, "package_managed"},
		{ErrNotWritable, "not_writable"},
		{context.DeadlineExceeded, "timeout"},
	} {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	return "error"
}
