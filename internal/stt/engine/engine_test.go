package engine

import (
	"slices"
	"testing"
)

func TestRegistry(t *testing.T) {
	Register("test-registry", func(Config) (Engine, Info, error) { return nil, Info{}, nil })
	if _, err := Lookup("test-registry"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(Names(), "test-registry") {
		t.Fatal("Names misses registered engine")
	}
	if _, err := Lookup("missing"); err == nil {
		t.Fatal("want error for unknown engine")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register must panic")
		}
	}()
	Register("test-registry", nil)
}
