package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// Checksums maps a file name to its lowercase hex SHA-256.
type Checksums map[string]string

// ParseChecksums reads sha256sum output: "<hex>  <name>" per line, with
// "*" before the name in binary mode.
func ParseChecksums(b []byte) (Checksums, error) {
	out := Checksums{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, " ")
		name = strings.TrimPrefix(strings.TrimLeft(name, " "), "*")
		if !ok || name == "" || len(sum) != sha256.Size*2 {
			return nil, fmt.Errorf("%w: checksums line %d malformed", ErrBadArchive, n)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, fmt.Errorf("%w: checksums line %d malformed", ErrBadArchive, n)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("%w: %s listed twice", ErrBadArchive, name)
		}
		out[name] = strings.ToLower(sum)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// VerifyFile checks that the file at path hashes to the entry for name.
func (c Checksums) VerifyFile(ctx context.Context, path, name string) error {
	want, ok := c[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrChecksumMissing, name)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: f}); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%w: %s", ErrChecksumMismatch, name)
	}
	return nil
}
