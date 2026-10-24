package models

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/xlsft/companion/internal/stt/engine"
)

// unpackSuffix is the directory an archive is unpacked into before it is
// renamed to the model directory.
const unpackSuffix = ".unpack"

// ErrArchive means the verified archive lacks a file the manifest names,
// or a member's size differs; the model is not installed.
var ErrArchive = errors.New("model archive does not match manifest")

// downloadArchive installs a multi-file model: the archive is fetched into
// <file>.part with the same resume and SHA-256 check as a single file, then
// streamed through bzip2 and tar, writing only the manifest's members into
// <id>.unpack, which is renamed to <id> once complete. The .part is kept
// until then, so an interrupted unpack restarts without downloading again.
func (d *Downloader) downloadArchive(ctx context.Context, m Manifest, destDir string, progress func(Progress)) (string, error) {
	final := filepath.Join(destDir, m.ID)
	if rec, err := readRecord(final + okSuffix); err == nil && rec.Dir && rec.SHA256 == m.SHA256 && present(rec, final) {
		emit(progress, Progress{Done: m.Size, Total: m.Size})
		return final, nil
	}
	_ = os.Remove(final + okSuffix)

	part := filepath.Join(destDir, m.File+partSuffix)
	h := sha256.New()
	have, err := hashExisting(ctx, part, h, m.Size)
	if err != nil {
		return "", err
	}
	// Archive and unpacked files coexist until the rename.
	if err := d.checkSpace(destDir, m.Size-have+m.UnpackedSize()); err != nil {
		return "", err
	}
	t := newTracker(d.clock, have, m.Size, progress)
	t.force(have)
	if have < m.Size {
		if have, err = d.fetch(ctx, m, part, h, have, t); err != nil {
			return "", err
		}
	}
	t.force(have)
	if got := hex.EncodeToString(h.Sum(nil)); got != m.SHA256 {
		_ = os.Remove(part)
		return "", fmt.Errorf("%w: %s", ErrChecksum, m.ID)
	}

	tmp := final + unpackSuffix
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	files, err := d.unpack(ctx, m, part, tmp, progress)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	desc := engine.Descriptor{Engine: m.Family, Type: m.ModelType, Kind: m.Kind, Files: map[string]string{}, Languages: m.Languages}
	if desc.Kind == "" {
		desc.Kind = KindOffline
	}
	for _, f := range m.Files {
		desc.Files[f.Role] = path.Base(f.Path)
	}
	if err := engine.WriteDescriptor(tmp, desc); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	syncDir(tmp)
	if err := os.RemoveAll(final); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	syncDir(destDir)
	rec := record{ID: m.ID, File: m.ID, Size: m.UnpackedSize(), SHA256: m.SHA256, Engine: m.Family,
		VerifiedAt: time.Now().UTC(), Dir: true, Files: files, Archive: m.File}
	if err := storeRecord(final, rec); err != nil {
		return "", err
	}
	_ = os.Remove(part)
	return final, nil
}

// unpack streams the archive and writes the manifest's members into dir,
// hashing each as it is written so the install record can re-verify the
// directory later without the archive.
func (d *Downloader) unpack(ctx context.Context, m Manifest, archive, dir string, progress func(Progress)) ([]InstalledFile, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	want := make(map[string]ArchiveFile, len(m.Files))
	for _, af := range m.Files {
		want[af.Path] = af
	}
	total := m.UnpackedSize()
	var done int64
	last := time.Time{}
	report := func(force bool) {
		if now := d.clock(); force || now.Sub(last) >= progressInterval {
			last = now
			emit(progress, Progress{Done: done, Total: total, Unpacking: true})
		}
	}
	report(true)

	got := map[string]InstalledFile{}
	tr := tar.NewReader(bzip2.NewReader(f))
	buf := make([]byte, bufSize)
	for len(got) < len(want) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrArchive, err)
		}
		af, ok := want[hdr.Name]
		if !ok || hdr.Typeflag != tar.TypeReg {
			continue // reading on skips the member's data
		}
		if hdr.Size != af.Size {
			return nil, fmt.Errorf("%w: %s is %d bytes", ErrArchive, af.Role, hdr.Size)
		}
		name := path.Base(af.Path)
		out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		var n int64
		for err == nil {
			if cerr := ctx.Err(); cerr != nil {
				err = cerr
				break
			}
			var k int
			k, err = tr.Read(buf)
			if k > 0 {
				if _, werr := out.Write(buf[:k]); werr != nil {
					err = werr
					break
				}
				h.Write(buf[:k])
				n += int64(k)
				done += int64(k)
				report(false)
			}
		}
		if err == io.EOF {
			err = nil
		}
		if err == nil {
			err = out.Sync()
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		if n != af.Size {
			return nil, fmt.Errorf("%w: %s truncated", ErrArchive, af.Role)
		}
		got[af.Path] = InstalledFile{Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	}
	files := make([]InstalledFile, 0, len(m.Files))
	for _, af := range m.Files {
		f, ok := got[af.Path]
		if !ok {
			return nil, fmt.Errorf("%w: %s missing", ErrArchive, af.Role)
		}
		files = append(files, f)
	}
	report(true)
	return files, nil
}
