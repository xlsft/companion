package engine

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestRegistry(t *testing.T) {
	// The registry is global; a unique name keeps -count=N runs independent.
	name := fmt.Sprintf("test-registry-%d", time.Now().UnixNano())
	Register(name, func(Config) (Engine, Info, error) { return nil, Info{}, nil })
	if _, err := Lookup(name); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(Names(), name) {
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
	Register(name, nil)
}
