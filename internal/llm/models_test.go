package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestNormalizeModels(t *testing.T) {
	many := make([]string, MaxModels+5)
	for i := range many {
		many[i] = fmt.Sprintf("m%04d", len(many)-i)
	}
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"trim, drop blank, dedupe", []string{" b ", "", "a", "b"}, []string{"a", "b"}},
		{"case-insensitive, stable ties", []string{"b", "B", "a", "A"}, []string{"A", "a", "B", "b"}},
	}
	for _, tt := range tests {
		if got := normalizeModels(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
	got := normalizeModels(many)
	if len(got) != MaxModels || got[0] != "m0001" {
		t.Errorf("cap: len %d, first %q", len(got), got[0])
	}
}

// Listing needs no model or name, but the endpoint rules still hold.
func TestListModelsValidates(t *testing.T) {
	tests := []struct {
		name  string
		cfg   ProviderConfig
		field string
	}{
		{"unknown kind", ProviderConfig{Kind: "x"}, "Kind"},
		{"plain http to a remote host", ProviderConfig{Kind: KindOpenAI, BaseURL: "http://api.example.com/v1"}, "BaseURL"},
		{"auth header", ProviderConfig{Kind: KindOpenAI, Headers: map[string]string{"Authorization": "x"}}, "Headers"},
	}
	for _, tt := range tests {
		_, err := ListModels(context.Background(), tt.cfg, "", nil)
		var ce *ConfigError
		if !errors.As(err, &ce) || ce.Field != tt.field {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
	// No adapter is linked into this package's tests.
	if _, err := ListModels(context.Background(), ProviderConfig{Kind: KindOpenAI}, "", nil); !errors.Is(err, ErrNoAdapter) {
		t.Errorf("no lister: err = %v", err)
	}
}
