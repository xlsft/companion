// Package update finds, downloads, verifies and installs new Companion
// releases published on GitHub (OC-75). Nothing is installed unless the
// release's checksums.txt carries a valid Sigstore signature from the
// project's release workflow and the tarball matches it.
package update

import (
	"fmt"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// CapSelfUpdate names in-app updating for contracts.Unsupported: only the
// Linux tarball is published so far.
const CapSelfUpdate contracts.Capability = "self_update"

// Release is a published GitHub release.
type Release struct {
	Tag        string    `json:"tag_name"`
	Name       string    `json:"name"`
	URL        string    `json:"html_url"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	Assets     []Asset   `json:"assets"`
	// Version is parsed from Tag by the checker.
	Version Version `json:"-"`
}

// Asset is a file attached to a release.
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// DownloadURL is the public link; APIURL serves the same bytes with
	// Accept: application/octet-stream and works with a token.
	DownloadURL string `json:"browser_download_url"`
	APIURL      string `json:"url"`
}

// Files that sign and checksum a release (the release contract).
const (
	ChecksumsName = "checksums.txt"
	SignatureName = "checksums.txt.sigstore.json"
)

// archName is the architecture as the release tarballs spell it.
func archName(goarch string) (string, bool) {
	switch goarch {
	case "amd64":
		return "x86_64", true
	case "arm64":
		return "aarch64", true
	}
	return "", false
}

// TarballName is the release asset for a version and platform.
func TarballName(v Version, goos, goarch string) (string, error) {
	arch, ok := archName(goarch)
	if goos != "linux" || !ok {
		return "", fmt.Errorf("update: no release build for %s/%s: %w", goos, goarch, contracts.Unsupported(CapSelfUpdate))
	}
	return fmt.Sprintf("companion-%s-linux-%s.tar.gz", v, arch), nil
}

// Bundle is the set of assets an install needs.
type Bundle struct {
	Tarball, Checksums, Signature Asset
}

// SelectAssets picks the tarball for goos/goarch and the files that
// vouch for it. A release without checksums or signature cannot be
// installed: there is no unsigned fallback.
func SelectAssets(r Release, goos, goarch string) (Bundle, error) {
	name, err := TarballName(r.Version, goos, goarch)
	if err != nil {
		return Bundle{}, err
	}
	return selectBundle(r, name)
}

// selectBundle picks the asset called name (a tarball or an AppImage) and
// the files that vouch for it.
func selectBundle(r Release, name string) (Bundle, error) {
	var b Bundle
	found := map[string]*Asset{name: &b.Tarball, ChecksumsName: &b.Checksums, SignatureName: &b.Signature}
	for _, a := range r.Assets {
		if dst, ok := found[a.Name]; ok {
			*dst = a
			delete(found, a.Name)
		}
	}
	switch {
	case found[name] != nil:
		return Bundle{}, fmt.Errorf("%w: %s", ErrNoAsset, name)
	case found[ChecksumsName] != nil:
		return Bundle{}, fmt.Errorf("%w: %s", ErrNoChecksums, ChecksumsName)
	case found[SignatureName] != nil:
		return Bundle{}, fmt.Errorf("%w: %s", ErrNoSignature, SignatureName)
	}
	return b, nil
}
