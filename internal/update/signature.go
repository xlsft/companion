package update

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
)

// The identity a release signature must carry: a keyless certificate
// issued to the release workflow of this repository, for a version tag.
const (
	ReleaseIdentity = `^https://github.com/xlsft/companion/.github/workflows/release.yml@refs/tags/v.*$`
	ReleaseIssuer   = "https://token.actions.githubusercontent.com"
)

// SignatureVerifier checks the Sigstore bundle of checksums.txt. It is an
// interface so the install decisions are testable without a real keyless
// signature, which cannot be produced offline.
type SignatureVerifier interface {
	VerifyChecksums(ctx context.Context, bundleJSON, checksums []byte) error
}

// Identity pins who may have signed: a SAN regexp and an exact issuer.
type Identity struct {
	SANRegex, Issuer string
}

// Sigstore verifies bundles against the public-good Sigstore instance.
type Sigstore struct {
	Identity Identity
	// Root provides the trusted root; nil uses PublicGoodRoot.
	Root func(ctx context.Context) (root.TrustedMaterial, error)
}

// NewSigstore pins the release workflow identity; cacheDir holds the
// TUF metadata of the trusted root ("" disables the disk cache).
func NewSigstore(cacheDir string, client *http.Client) *Sigstore {
	return &Sigstore{
		Identity: Identity{SANRegex: ReleaseIdentity, Issuer: ReleaseIssuer},
		Root:     func(ctx context.Context) (root.TrustedMaterial, error) { return PublicGoodRoot(ctx, cacheDir, client) },
	}
}

func (s *Sigstore) VerifyChecksums(ctx context.Context, bundleJSON, checksums []byte) error {
	sum := sha256.Sum256(checksums)
	return s.VerifyDigest(ctx, bundleJSON, sum[:])
}

// VerifyDigest checks that bundleJSON signs the SHA-256 digest of an
// artifact with the pinned identity.
func (s *Sigstore) VerifyDigest(ctx context.Context, bundleJSON, digest []byte) error {
	if len(bundleJSON) == 0 {
		return ErrNoSignature
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	get := s.Root
	if get == nil {
		get = func(ctx context.Context) (root.TrustedMaterial, error) { return PublicGoodRoot(ctx, "", nil) }
	}
	tm, err := get(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The public-good instance: certificates carry SCTs and every
	// signature is in Rekor, timed by the log or a timestamp authority.
	v, err := verify.NewVerifier(tm,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	id, err := verify.NewShortCertificateIdentity(s.Identity.Issuer, "", "", s.Identity.SANRegex)
	if err != nil {
		return err
	}
	// The signed digest must equal the digest of exactly these bytes.
	if _, err := v.Verify(&b, verify.NewPolicy(verify.WithArtifactDigest("sha256", digest), verify.WithCertificateIdentity(id))); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	return nil
}

// embeddedRoot is the public-good trusted root as of this build. It is
// used when the TUF repository cannot be reached, so an offline machine
// can still verify releases signed before the root next rotates.
//
//go:embed trusted_root.json
var embeddedRoot []byte

// PublicGoodRoot fetches the current trusted root through Sigstore's TUF
// repository (cached in cacheDir for a day) and falls back to the root
// embedded at build time. A fetch never outlives ctx.
func PublicGoodRoot(ctx context.Context, cacheDir string, client *http.Client) (root.TrustedMaterial, error) {
	if b, err := fetchTUFRoot(ctx, cacheDir, client); err == nil {
		if tr, err := root.NewTrustedRootFromJSON(b); err == nil {
			return tr, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return root.NewTrustedRootFromJSON(embeddedRoot)
}

func fetchTUFRoot(ctx context.Context, cacheDir string, client *http.Client) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	opts := tuf.DefaultOptions().WithContext(ctx)
	if cacheDir == "" {
		opts = opts.WithDisableLocalCache()
	} else {
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			return nil, err
		}
		opts = opts.WithCachePath(filepath.Join(cacheDir, "tuf")).WithCacheValidity(1)
	}
	f := fetcher.NewDefaultFetcher().NewFetcherWithHTTPClient(ctxClient{ctx: ctx, c: client})
	f.SetRetry(time.Second, 1)
	opts = opts.WithFetcher(f)
	type res struct {
		b   []byte
		err error
	}
	// The TUF client has no context of its own; the fetcher's requests are
	// bound to ctx and the result is abandoned once ctx is done.
	ch := make(chan res, 1)
	go func() {
		c, err := tuf.New(opts)
		if err != nil {
			ch <- res{err: err}
			return
		}
		b, err := c.GetTarget("trusted_root.json")
		ch <- res{b, err}
	}()
	select {
	case r := <-ch:
		return r.b, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type ctxClient struct {
	ctx context.Context
	c   *http.Client
}

func (c ctxClient) Do(req *http.Request) (*http.Response, error) {
	return c.c.Do(req.WithContext(c.ctx))
}
