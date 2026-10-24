package update

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
)

// cosign v3.1.4's own release bundle for cosign-linux-amd64, recorded from
// github.com/sigstore/cosign/releases. It is a real keyless message
// signature in the new bundle format, which cannot be minted offline;
// the digest is the binary's SHA-256 as GitHub lists it.
const (
	fixtureBundle  = "testdata/cosign-linux-amd64.sigstore.json"
	fixtureDigest  = "424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa"
	fixtureSAN     = `^keyless@projectsigstore\.iam\.gserviceaccount\.com$`
	fixtureIssuer  = "https://accounts.google.com"
	otherDigestHex = "0000000000000000000000000000000000000000000000000000000000000000"
)

// embeddedOnly verifies offline against the root shipped in the binary.
func embeddedOnly(context.Context) (root.TrustedMaterial, error) {
	return root.NewTrustedRootFromJSON(embeddedRoot)
}

func TestSigstoreRecordedBundle(t *testing.T) {
	b, err := os.ReadFile(fixtureBundle)
	if err != nil {
		t.Fatal(err)
	}
	good, _ := hex.DecodeString(fixtureDigest)
	other, _ := hex.DecodeString(otherDigestHex)
	cosign := Identity{SANRegex: fixtureSAN, Issuer: fixtureIssuer}
	release := Identity{SANRegex: ReleaseIdentity, Issuer: ReleaseIssuer}
	tests := []struct {
		name   string
		id     Identity
		bundle []byte
		digest []byte
		want   error
	}{
		{"valid", cosign, b, good, nil},
		{"digest of another file", cosign, b, other, ErrBadSignature},
		{"signed by someone else than the release workflow", release, b, good, ErrBadSignature},
		{"wrong issuer", Identity{SANRegex: fixtureSAN, Issuer: ReleaseIssuer}, b, good, ErrBadSignature},
		{"missing", cosign, nil, good, ErrNoSignature},
		{"garbage", cosign, []byte(`{"mediaType":"nope"}`), good, ErrBadSignature},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Sigstore{Identity: tt.id, Root: embeddedOnly}
			err := s.VerifyDigest(context.Background(), tt.bundle, tt.digest)
			if tt.want == nil && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestPublicGoodRootHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PublicGoodRoot(ctx, t.TempDir(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
