package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/xlsft/companion/internal/diagnostics/logging"
)

// ErrSecretDetected is returned by Export when the final redaction pass still
// finds a secret-looking value. The message names only the file.
var ErrSecretDetected = errors.New("diagnostics: secret pattern found in export")

// PreviewEntry is one file of the archive exactly as it will be written
// (FR-DATA-007: the UI shows this before the user saves).
type PreviewEntry struct {
	Name string `json:"name"`
	Size int    `json:"size"`
	Text string `json:"text"`
}

type manifestFile struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

type manifest struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Files       []manifestFile `json:"files"`
}

// Preview renders the archive contents without writing anything. The
// manifest lists the other files; it cannot list its own size. Marshalling
// these plain types cannot fail in practice; on failure Preview returns nil
// and Export reports the error.
func Preview(r Report) []PreviewEntry {
	e, _ := preview(r)
	return e
}

func preview(r Report) ([]PreviewEntry, error) {
	rep, err := marshal(r)
	if err != nil {
		return nil, err
	}
	caps, err := marshal(r.Capabilities)
	if err != nil {
		return nil, err
	}
	var jl bytes.Buffer
	for _, e := range r.RecentErrors {
		line, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		jl.Write(line)
		jl.WriteByte('\n')
	}

	entries := []PreviewEntry{
		entry("report.json", rep),
		entry("capabilities.json", caps),
		entry("errors.jsonl", jl.Bytes()),
	}
	m := manifest{GeneratedAt: r.GeneratedAt}
	for _, e := range entries {
		m.Files = append(m.Files, manifestFile{Name: e.Name, Size: e.Size})
	}
	mb, err := marshal(m)
	if err != nil {
		return nil, err
	}
	return append(entries, entry("manifest.json", mb)), nil
}

func entry(name string, b []byte) PreviewEntry {
	return PreviewEntry{Name: name, Size: len(b), Text: string(b)}
}

func marshal(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Export writes the diagnostic archive (zip) to w. Every file passes through
// the redaction check first; nothing is written if a secret survives.
func Export(r Report, w io.Writer) error {
	entries, err := preview(r)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if logging.ContainsSecret(e.Text) {
			return fmt.Errorf("%w: %s", ErrSecretDetected, e.Name)
		}
	}
	zw := zip.NewWriter(w)
	for _, e := range entries {
		// Fixed modification time keeps the archive reproducible for a report.
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: e.Name, Method: zip.Deflate, Modified: r.GeneratedAt})
		if err != nil {
			return err
		}
		if _, err := io.WriteString(fw, e.Text); err != nil {
			return err
		}
	}
	return zw.Close()
}
