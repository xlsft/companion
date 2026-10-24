package models

import (
	"strings"
	"testing"
)

func goodManifest() Manifest {
	return Manifest{ID: "a", Family: "whisper", File: "a.bin", URL: "https://x.test/a.bin", Size: 1,
		SHA256: strings.Repeat("a", 64), Languages: []string{"multi"}}
}

func TestBuiltinCatalogValid(t *testing.T) {
	c, err := BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(c) < 10 {
		t.Fatalf("catalog too small: %d", len(c))
	}
	if _, ok := c.Find("whisper-small"); !ok {
		t.Fatal("whisper-small missing")
	}
	for _, id := range []string{"parakeet-tdt-0.6b-v3-int8", "parakeet-tdt-0.6b-v2-int8", "sense-voice-small-int8",
		"zipformer-streaming-en-int8", "zipformer-streaming-zh-en-int8"} {
		m, ok := c.Find(id)
		if !ok || m.Family != FamilySherpa || !m.Archive() || m.Fallback || m.Hidden || m.License == "" {
			t.Errorf("%s: %+v", id, m)
		}
	}
	for _, m := range c {
		if m.Family == FamilyWhisper && !m.Fallback {
			t.Errorf("%s: whisper models are fallbacks", m.ID)
		}
	}
}

func TestManifestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
		ok     bool
	}{
		{"good", func(*Manifest) {}, true},
		{"empty id", func(m *Manifest) { m.ID = "" }, false},
		{"path id", func(m *Manifest) { m.ID = "../x" }, false},
		{"http url", func(m *Manifest) { m.URL = "http://x.test/a" }, false},
		{"short sha", func(m *Manifest) { m.SHA256 = "abc" }, false},
		{"upper sha", func(m *Manifest) { m.SHA256 = strings.Repeat("A", 64) }, false},
		{"path file", func(m *Manifest) { m.File = "../a.bin" }, false},
		{"zero size", func(m *Manifest) { m.Size = 0 }, false},
		{"no langs", func(m *Manifest) { m.Languages = nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := goodManifest()
			tt.mutate(&m)
			if err := m.Validate(); (err == nil) != tt.ok {
				t.Fatalf("err=%v want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestCatalogDuplicateID(t *testing.T) {
	m := goodManifest()
	if err := (Catalog{m, m}).Validate(); err == nil {
		t.Fatal("want duplicate error")
	}
}
