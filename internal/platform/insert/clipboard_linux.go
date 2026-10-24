//go:build linux

package insert

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"

	"github.com/xlsft/companion/pkg/contracts"
)

// cmdClipboard drives the clipboard through wl-clipboard (Wayland) or
// xclip (X11). Text and saved content go through stdin and stdout only,
// never through arguments other processes could read.
type cmdClipboard struct {
	wayland bool
	look    func(string) (string, error)
	// run runs a tool with stdin and returns its stdout; for tools that
	// fork a server (wl-copy, xclip -i) stdout is discarded.
	run func(ctx context.Context, stdin []byte, keepOut bool, name string, args ...string) ([]byte, error)
}

func newCmdClipboard(wayland bool) *cmdClipboard {
	return &cmdClipboard{wayland: wayland, look: exec.LookPath, run: runTool}
}

// maxClip bounds what is saved for restoring; anything bigger is left
// alone and the text typed instead.
const maxClip = 16 << 20

const textMime = "text/plain;charset=utf-8"

func runTool(ctx context.Context, stdin []byte, keepOut bool, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	if keepOut {
		cmd.Stdout = &limited{w: &out, n: maxClip + 1}
	}
	err := cmd.Run()
	return out.Bytes(), err
}

// limited stops collecting after n bytes, so a huge clipboard is detected
// without being held in memory.
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	k := min(len(p), l.n)
	l.n -= k
	_, err := l.w.Write(p[:k])
	return len(p), err
}

func (c *cmdClipboard) tools() (read, write string, ok bool) {
	if c.wayland {
		read, write = "wl-paste", "wl-copy"
	} else {
		read, write = "xclip", "xclip"
	}
	_, e1 := c.look(read)
	_, e2 := c.look(write)
	return read, write, e1 == nil && e2 == nil
}

func (c *cmdClipboard) Save(ctx context.Context) (Clip, error) {
	read, _, ok := c.tools()
	if !ok {
		return Clip{}, contracts.Unsupported(contracts.CapTextInsertion)
	}
	var typesOut []byte
	var err error
	if c.wayland {
		typesOut, err = c.run(ctx, nil, true, read, "--list-types")
	} else {
		typesOut, err = c.run(ctx, nil, true, read, "-selection", "clipboard", "-o", "-t", "TARGETS")
	}
	if ctx.Err() != nil {
		return Clip{}, ctx.Err()
	}
	types := pickTypes(string(typesOut))
	if err != nil || len(types) == 0 {
		// Both tools fail when nothing owns the clipboard.
		return Clip{Empty: true}, nil
	}
	mime := types[0]
	var data []byte
	if c.wayland {
		data, err = c.run(ctx, nil, true, read, "--no-newline", "--type", mime)
	} else {
		data, err = c.run(ctx, nil, true, read, "-selection", "clipboard", "-o", "-t", mime)
	}
	if err != nil {
		if ctx.Err() != nil {
			return Clip{}, ctx.Err()
		}
		return Clip{}, contracts.Unsupported(contracts.CapTextInsertion)
	}
	if len(data) > maxClip {
		// Could not be put back whole: leave the clipboard alone.
		return Clip{}, contracts.Unsupported(contracts.CapTextInsertion)
	}
	return Clip{Mime: mime, Data: data}, nil
}

// pickTypes orders the offered types: text first (what a paste restores
// most faithfully), and without X11 meta targets.
func pickTypes(out string) []string {
	var types []string
	for _, t := range strings.Fields(out) {
		switch t {
		case "TARGETS", "MULTIPLE", "TIMESTAMP", "SAVE_TARGETS", "DELETE", "INSERT_PROPERTY", "INSERT_SELECTION":
			continue
		}
		types = append(types, t)
	}
	for _, pref := range []string{textMime, "UTF8_STRING", "text/plain"} {
		if i := slices.Index(types, pref); i > 0 {
			types = append([]string{pref}, slices.Delete(types, i, i+1)...)
			break
		}
	}
	return types
}

func (c *cmdClipboard) write(ctx context.Context, mime string, data []byte) error {
	_, write, ok := c.tools()
	if !ok {
		return contracts.Unsupported(contracts.CapTextInsertion)
	}
	var err error
	if c.wayland {
		_, err = c.run(ctx, data, false, write, "--type", mime)
	} else {
		_, err = c.run(ctx, data, false, write, "-selection", "clipboard", "-i", "-t", mime)
	}
	return err
}

func (c *cmdClipboard) SetText(ctx context.Context, text string) error {
	mime := textMime
	if !c.wayland {
		mime = "UTF8_STRING"
	}
	return c.write(ctx, mime, []byte(text))
}

func (c *cmdClipboard) Restore(ctx context.Context, cl Clip) error {
	if !cl.Empty {
		return c.write(ctx, cl.Mime, cl.Data)
	}
	if c.wayland {
		_, write, ok := c.tools()
		if !ok {
			return contracts.Unsupported(contracts.CapTextInsertion)
		}
		_, err := c.run(ctx, nil, false, write, "--clear")
		return err
	}
	// xclip cannot give up ownership; an empty string is the closest.
	err := c.write(ctx, "UTF8_STRING", nil)
	if errors.Is(err, contracts.ErrUnsupported) {
		return nil
	}
	return err
}
