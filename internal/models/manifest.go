// Package models describes the downloadable STT models, ships the built-in
// catalog and installs models safely (OC-31, OC-32).
package models

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// LangMulti marks a model that handles many languages.
const LangMulti = "multi"

// Manifest describes one downloadable model file.
type Manifest struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// Family is the runtime that can load the file; only "whisper" today.
	Family string `json:"family"`
	// File is the on-disk name; it must be a bare file name.
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

// BuiltinCatalog returns the embedded official whisper.cpp catalog.
func BuiltinCatalog() (Catalog, error) { return ParseCatalog(catalogJSON) }
