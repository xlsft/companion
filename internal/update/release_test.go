package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xlsft/companion/pkg/contracts"
)

func TestSelectAssets(t *testing.T) {
	v := Version{Major: 0, Minor: 2, Patch: 0}
	all := []Asset{
		{Name: "companion-0.2.0-linux-x86_64.tar.gz"},
		{Name: "companion-0.2.0-linux-aarch64.tar.gz"},
		{Name: "Companion-0.2.0-x86_64.AppImage"},
		{Name: ChecksumsName},
		{Name: SignatureName},
	}
	without := func(name string) []Asset {
		var out []Asset
		for _, a := range all {
			if a.Name != name {
				out = append(out, a)
			}
		}
		return out
	}
	tests := []struct {
		name         string
		goos, goarch string
		assets       []Asset
		want         string
		err          error
	}{
		{"linux amd64", "linux", "amd64", all, "companion-0.2.0-linux-x86_64.tar.gz", nil},
		{"linux arm64", "linux", "arm64", all, "companion-0.2.0-linux-aarch64.tar.gz", nil},
		{"no build for arch", "linux", "arm64", without("companion-0.2.0-linux-aarch64.tar.gz"), "", ErrNoAsset},
		{"no checksums", "linux", "amd64", without(ChecksumsName), "", ErrNoChecksums},
		{"no signature", "linux", "amd64", without(SignatureName), "", ErrNoSignature},
		{"windows", "windows", "amd64", all, "", contracts.ErrUnsupported},
		{"darwin", "darwin", "arm64", all, "", contracts.ErrUnsupported},
		{"linux 386", "linux", "386", all, "", contracts.ErrUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := SelectAssets(Release{Version: v, Assets: tt.assets}, tt.goos, tt.goarch)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if b.Tarball.Name != tt.want || b.Checksums.Name != ChecksumsName || b.Signature.Name != SignatureName {
				t.Errorf("bundle = %+v", b)
			}
		})
	}
}

func TestChecksums(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(file, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const helloSum = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	const other = "0000000000000000000000000000000000000000000000000000000000000000"
	tests := []struct {
		name string
		sums string
		err  error // from parse or verify
	}{
		{"good", helloSum + "  a.tar.gz\n" + other + "  b.tar.gz\n", nil},
		{"good binary mode", helloSum + " *a.tar.gz\n", nil},
		{"good upper case", "5891B5B522D5DF086D0FF0B110FBD9D21BB4FC7163AF34D08286A2E846F6BE03  a.tar.gz\n", nil},
		{"bad", other + "  a.tar.gz\n", ErrChecksumMismatch},
		{"missing", other + "  b.tar.gz\n", ErrChecksumMissing},
		{"malformed", "abc  a.tar.gz\n", ErrBadArchive},
		{"listed twice", helloSum + "  a.tar.gz\n" + helloSum + "  a.tar.gz\n", ErrBadArchive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs, err := ParseChecksums([]byte(tt.sums))
			if err == nil {
				err = cs.VerifyFile(context.Background(), file, "a.tar.gz")
			}
			if tt.err == nil && err != nil {
				t.Fatal(err)
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
		})
	}
}
