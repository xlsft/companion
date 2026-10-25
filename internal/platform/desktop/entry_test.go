package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepairEntry(t *testing.T) {
	const entry = "[Desktop Entry]\nType=Application\nName=Companion\nExec=companion\nIcon=companion\n"
	tests := []struct {
		name    string
		content string // "" for no file
		exe     string
		changed bool
		want    string
	}{
		{"bare name is fixed", entry, "/home/u/.local/lib/companion/companion", true,
			"[Desktop Entry]\nType=Application\nName=Companion\nExec=/home/u/.local/lib/companion/companion\nIcon=companion\n"},
		{"path with a space is quoted", entry, "/home/my user/lib/companion", true,
			"[Desktop Entry]\nType=Application\nName=Companion\nExec=\"/home/my user/lib/companion\"\nIcon=companion\n"},
		{"already absolute is left", "[Desktop Entry]\nExec=\"/opt/c/companion\"\n", "/home/u/c", false, "[Desktop Entry]\nExec=\"/opt/c/companion\"\n"},
		{"someone else's command is left", "[Desktop Entry]\nExec=companion --x\n", "/home/u/c", false, "[Desktop Entry]\nExec=companion --x\n"},
		{"no entry", "", "/home/u/c", false, ""},
		{"relative exe is ignored", entry, "companion", false, entry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			p := filepath.Join(home, "applications", EntryName)
			if tt.content != "" {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			changed, err := RepairEntry(home, tt.exe)
			if err != nil || changed != tt.changed {
				t.Fatalf("changed = %v, err = %v", changed, err)
			}
			if tt.content == "" {
				return
			}
			got, _ := os.ReadFile(p)
			if string(got) != tt.want {
				t.Errorf("entry =\n%s\nwant\n%s", got, tt.want)
			}
			if _, err := os.Stat(p + ".new"); err == nil {
				t.Error("temporary file left behind")
			}
		})
	}
}
