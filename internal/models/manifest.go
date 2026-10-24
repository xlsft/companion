// Package models describes the downloadable STT models, ships the built-in
// catalog and installs models safely (OC-31, OC-32).
package models

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// LangMulti marks a model that handles many languages.
const LangMulti = "multi"

// Model families: the engine that loads a model.
const (
	FamilySherpa  = "sherpa"
	FamilyWhisper = "whisper"
)

// FormatTarBz2 is a multi-file model shipped as one archive.
const FormatTarBz2 = "tar.bz2"

// Kinds of recognition, see engine.KindOffline/KindStreaming.
const (
	KindOffline   = "offline"
	KindStreaming = "streaming"
)

// ArchiveFile is one archive member a model needs. The rest of the
// archive (test audio, full-precision copies, export scripts) is skipped.
type ArchiveFile struct {
	// Role is an engine.Descriptor role: encoder, decoder, joiner, model, tokens.
	Role string `json:"role"`
	// Path is the member's name in the archive; it is installed under its
	// base name.
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Manifest describes one downloadable model: a single file, or an archive
// that is unpacked into a directory named after the ID.
type Manifest struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// Family is the runtime that can load the model: FamilySherpa or
	// FamilyWhisper.
	Family string `json:"family"`
	// File is the downloaded name (the model or its archive); it must be a
	// bare file name.
	File   string `json:"file"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Languages holds "multi" or ISO codes the model supports.
	Languages    []string `json:"languages"`
	Quantization string   `json:"quantization,omitempty"`
	// RAMRequiredMB is a rough working-set estimate used for recommendations.
	RAMRequiredMB int `json:"ram_required_mb"`
	// Backends lists compute backends that can run it: cpu, cuda, vulkan, metal.
	Backends          []string `json:"backends"`
	MinRuntimeVersion string   `json:"min_runtime_version,omitempty"`

	// Format is "" for a single model file or FormatTarBz2; Size and SHA256
	// then describe the archive.
	Format string `json:"format,omitempty"`
	// Files are the members to install from the archive.
	Files []ArchiveFile `json:"files,omitempty"`
	// Kind is KindOffline or KindStreaming; empty means offline.
	Kind string `json:"kind,omitempty"`
	// ModelType is the engine's architecture (engine.Descriptor.Type).
	ModelType string `json:"model_type,omitempty"`
	// Fallback marks a model meant only for languages no primary model
	// covers; the recommender prefers any primary model that fits.
	Fallback bool `json:"fallback,omitempty"`
	// Hidden keeps a model out of pickers unless it is installed, chosen
	// or recommended; it still resolves by ID.
	Hidden bool `json:"hidden,omitempty"`
	// License is the model's licence (SPDX id where one exists).
	License string `json:"license,omitempty"`
}

// Archive reports whether the model is an archive unpacked into a directory.
func (m Manifest) Archive() bool { return m.Format == FormatTarBz2 }

// Streaming reports whether the model decodes incrementally.
func (m Manifest) Streaming() bool { return m.Kind == KindStreaming }

// UnpackedSize is the disk space the installed model takes.
func (m Manifest) UnpackedSize() int64 {
	if !m.Archive() {
		return m.Size
	}
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

// Supports reports whether the model recognises lang (an ISO code);
// "auto" and "" are supported by multilingual models only.
func (m Manifest) Supports(lang string) bool {
	if m.Multilingual() {
		return true
	}
	for _, l := range m.Languages {
		if l == lang {
			return true
		}
	}
	return false
}

// Multilingual reports whether the model is not English-only.
func (m Manifest) Multilingual() bool {
	for _, l := range m.Languages {
		if l == LangMulti {
			return true
		}
	}
	return false
}

// Validate checks a single manifest.
func (m Manifest) Validate() error {
	switch {
	case m.ID == "":
		return errors.New("empty id")
	case m.Family == "":
		return fmt.Errorf("%s: empty family", m.ID)
	case !bareName(m.ID) || !bareName(m.File):
		// Both end up in file names, so path tricks must never pass.
		return fmt.Errorf("%s: id and file must be bare names", m.ID)
	case m.Size <= 0:
		return fmt.Errorf("%s: size must be positive", m.ID)
	case len(m.Languages) == 0:
		return fmt.Errorf("%s: no languages", m.ID)
	}
	if !validSHA256(m.SHA256) {
		return fmt.Errorf("%s: sha256 must be 64 lowercase hex chars", m.ID)
	}
	u, err := url.Parse(m.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s: url must be https", m.ID)
	}
	if m.Kind != "" && m.Kind != KindOffline && m.Kind != KindStreaming {
		return fmt.Errorf("%s: kind %q", m.ID, m.Kind)
	}
	switch m.Format {
	case "":
		if len(m.Files) > 0 {
			return fmt.Errorf("%s: files need an archive format", m.ID)
		}
		return nil
	case FormatTarBz2:
	default:
		return fmt.Errorf("%s: format %q", m.ID, m.Format)
	}
	if m.ModelType == "" || len(m.Files) == 0 {
		return fmt.Errorf("%s: archive needs model_type and files", m.ID)
	}
	names := map[string]bool{}
	roles := map[string]bool{}
	for _, f := range m.Files {
		name := path.Base(f.Path)
		switch {
		case f.Role == "" || roles[f.Role]:
			return fmt.Errorf("%s: file roles must be set and unique", m.ID)
		case f.Size <= 0:
			return fmt.Errorf("%s: %s size must be positive", m.ID, f.Role)
		case path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || strings.HasPrefix(f.Path, "../") || !bareName(name) || names[name]:
			// Members install under their base name, so those must be
			// distinct and nothing may point outside the directory.
			return fmt.Errorf("%s: bad archive path %q", m.ID, f.Path)
		}
		names[name], roles[f.Role] = true, true
	}
	if !roles["tokens"] {
		return fmt.Errorf("%s: archive needs a tokens file", m.ID)
	}
	return nil
}

func bareName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Catalog is an ordered list of manifests.
type Catalog []Manifest

// ParseCatalog decodes and validates a JSON catalog.
func ParseCatalog(data []byte) (Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks every manifest and id uniqueness.
func (c Catalog) Validate() error {
	seen := make(map[string]bool, len(c))
	for _, m := range c {
		if err := m.Validate(); err != nil {
			return err
		}
		if seen[m.ID] {
			return fmt.Errorf("duplicate id %q", m.ID)
		}
		seen[m.ID] = true
	}
	return nil
}

// Find returns the manifest with the given id.
func (c Catalog) Find(id string) (Manifest, bool) {
	for _, m := range c {
		if m.ID == id {
			return m, true
		}
	}
	return Manifest{}, false
}

//go:embed catalog.json
var catalogJSON []byte

// BuiltinCatalog returns the embedded catalog: sherpa-onnx models from the
// k2-fsa/sherpa-onnx asr-models release first, then the whisper.cpp
// fallbacks.
func BuiltinCatalog() (Catalog, error) { return ParseCatalog(catalogJSON) }
