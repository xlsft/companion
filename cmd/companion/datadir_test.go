package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Data under the old name moves to the new folder once; an existing new
// folder is never overwritten.
func TestAdoptLegacyDir(t *testing.T) {
	tests := []struct {
		name          string
		old, current  bool
		wantFrom      string // file expected inside the result
		wantOldRemain bool
	}{
		{"fresh", false, false, "", false},
		{"old only", true, false, "old", false},
		{"new only", false, true, "new", false},
		{"both", true, true, "new", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			mk := func(name, mark string) {
				d := filepath.Join(base, name)
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(d, mark), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.old {
				mk("openclick", "old")
			}
			if tt.current {
				mk("companion", "new")
			}
			dir, err := adoptLegacyDir(base)
			if err != nil {
				t.Fatal(err)
			}
			if dir != filepath.Join(base, "companion") {
				t.Fatalf("dir %s", dir)
			}
			if tt.wantFrom != "" {
				if _, err := os.Stat(filepath.Join(dir, tt.wantFrom)); err != nil {
					t.Errorf("%s not in %s", tt.wantFrom, dir)
				}
			}
			_, err = os.Stat(filepath.Join(base, "openclick"))
			if (err == nil) != tt.wantOldRemain {
				t.Errorf("old folder remains = %v, want %v", err == nil, tt.wantOldRemain)
			}
		})
	}
}
