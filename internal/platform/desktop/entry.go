package desktop

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EntryName is the desktop entry the Linux installers write.
const EntryName = "companion.desktop"

// RepairEntry makes the user's own desktop entry start exe by its full
// path. Launchers run entries with the session's PATH, which often lacks
// ~/.local/bin, so an entry with a bare "Exec=companion" (written by
// installers before v0.0.3) does nothing when clicked. Only the entry in
// dataHome (XDG_DATA_HOME, usually ~/.local/share) is touched, only when
// its Exec is that bare name, and it is replaced in one rename. It reports
// whether the file changed; a missing entry is not an error.
func RepairEntry(dataHome, exe string) (bool, error) {
	if dataHome == "" || !filepath.IsAbs(exe) {
		return false, nil
	}
	p := filepath.Join(dataHome, "applications", EntryName)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lines := bytes.Split(b, []byte("\n"))
	changed := false
	for i, l := range lines {
		if string(bytes.TrimRight(l, "\r")) == "Exec=companion" {
			lines[i] = []byte("Exec=" + execQuote(exe))
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, bytes.Join(lines, []byte("\n")), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// execQuote quotes a path for an Exec key as the Desktop Entry spec asks:
// in double quotes with ", `, $ and \ escaped, when it has anything but
// plain characters.
func execQuote(path string) string {
	if !strings.ContainsAny(path, " \t\"'`$\\><~|&;*?#()") {
		return path
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range path {
		if strings.ContainsRune("\"`$\\", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// DataHome is XDG_DATA_HOME, or ~/.local/share.
func DataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}
