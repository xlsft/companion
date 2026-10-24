package keybind

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Marker words; the comment syntax goes in front.
const (
	startMark = ">>> companion"
	endMark   = "<<< companion"
	// BackupSuffix names the one-time copy of a file before Companion's
	// first edit.
	BackupSuffix = ".companion.bak"
)

// block wraps body in marker comments of the given comment prefix.
func block(comment, body string) string {
	var b strings.Builder
	b.WriteString(comment + " " + startMark + " (managed by Companion: replaced when you apply its shortcuts)\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(comment + " " + endMark + "\n")
	return b.String()
}

func isMark(line, comment, mark string) bool {
	t := strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(t, comment)
	return ok && strings.HasPrefix(strings.TrimSpace(rest), mark)
}

// findBlock returns the byte range of the marked block (whole lines,
// including both markers) or ok=false when there is none.
func findBlock(content, comment string) (start, end int, ok bool, err error) {
	start = -1
	off := 0
	for _, line := range strings.SplitAfter(content, "\n") {
		switch {
		case start < 0 && isMark(line, comment, startMark):
			start = off
		case start >= 0 && isMark(line, comment, endMark):
			return start, off + len(line), true, nil
		case start >= 0 && isMark(line, comment, startMark):
			return 0, 0, false, fail(CodeDamagedBlock, errors.New("nested start marker"))
		}
		off += len(line)
	}
	if start >= 0 {
		return 0, 0, false, fail(CodeDamagedBlock, errors.New("start marker without end"))
	}
	return 0, 0, false, nil
}

// upsert puts blk (from block) into content: in place of an existing
// marked block, else appended after a blank line. Everything outside the
// block is kept byte for byte.
func upsert(content, comment, blk string) (string, error) {
	s, e, ok, err := findBlock(content, comment)
	if err != nil {
		return "", err
	}
	if ok {
		return content[:s] + blk + content[e:], nil
	}
	switch {
	case content == "":
	case strings.HasSuffix(content, "\n\n"):
	case strings.HasSuffix(content, "\n"):
		content += "\n"
	default:
		content += "\n\n"
	}
	return content + blk, nil
}

// withoutBlock is content with the marked block cut out, for scanning
// what the user bound themselves.
func withoutBlock(content, comment string) string {
	s, e, ok, err := findBlock(content, comment)
	if err != nil || !ok {
		return content
	}
	return content[:s] + content[e:]
}

// fileHasBlock reports whether path holds exactly blk as its marked block.
func fileHasBlock(path, blk string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	comment := strings.Fields(blk)[0]
	s, e, ok, err := findBlock(string(b), comment)
	return err == nil && ok && string(b[s:e]) == blk
}

// edit is one pending change of a config file.
type edit struct {
	path string // as configured; may be a symlink
	real string // the file actually written
	old  []byte
	new  []byte
	mode fs.FileMode
	// created: the file did not exist (a drop-in).
	created bool
}

// prepare reads path and computes the new content. create allows a
// missing file (drop-ins); a missing main config is an error, because
// writing one would replace the desktop's whole default configuration.
func prepare(path, comment, blk string, create bool) (*edit, error) {
	ed := &edit{path: path, real: path, mode: 0o644}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		ed.real = r
	}
	st, err := os.Stat(ed.real)
	switch {
	case errors.Is(err, fs.ErrNotExist) && create:
		ed.created = true
	case errors.Is(err, fs.ErrNotExist):
		return nil, fail(CodeConfigMissing, err)
	case err != nil:
		return nil, fail(CodeReadFailed, err)
	default:
		ed.mode = st.Mode().Perm()
		if ed.old, err = os.ReadFile(ed.real); err != nil {
			return nil, fail(CodeReadFailed, err)
		}
	}
	out, err := upsert(string(ed.old), comment, blk)
	if err != nil {
		return nil, err
	}
	ed.new = []byte(out)
	return ed, nil
}

func (ed *edit) changed() bool { return ed.created || !bytes.Equal(ed.old, ed.new) }

// commit backs the original up once and writes the new content
// atomically, keeping the file's mode.
func (ed *edit) commit() error {
	if !ed.changed() {
		return nil
	}
	if !ed.created {
		bak := ed.real + BackupSuffix
		if _, err := os.Lstat(bak); errors.Is(err, fs.ErrNotExist) {
			if err := writeAtomic(bak, ed.old, ed.mode); err != nil {
				return fail(CodeWriteFailed, fmt.Errorf("backup: %w", err))
			}
		}
	} else if err := os.MkdirAll(filepath.Dir(ed.real), 0o755); err != nil {
		return fail(CodeWriteFailed, err)
	}
	if err := writeAtomic(ed.real, ed.new, ed.mode); err != nil {
		return fail(CodeWriteFailed, err)
	}
	return nil
}

// rollback restores what commit replaced, after the desktop rejected it.
func (ed *edit) rollback() error {
	if ed.created {
		return os.Remove(ed.real)
	}
	return writeAtomic(ed.real, ed.old, ed.mode)
}

// scratch writes content to a hidden file next to the configured path,
// where relative includes resolve as for the real file, for a validator.
func scratch(path string, content []byte) (string, error) {
	p := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".companion-"+randHex()+filepath.Ext(path))
	if err := os.WriteFile(p, content, 0o600); err != nil {
		return "", fail(CodeWriteFailed, err)
	}
	return p, nil
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".companion-tmp-"+randHex())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	// WriteFile honours the umask; the original mode must survive.
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func randHex() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
