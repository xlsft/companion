package keybind

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xlsft/companion/internal/platform/keys"
	"github.com/xlsft/companion/pkg/contracts"
)

// niri binds fire on press only and repeat while held unless
// repeat=false, which would toggle Talk on and off: so toggle, no repeat.

func niriPath(env Env) string {
	if p := env.getenv("NIRI_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(env.ConfigHome, "niri", "config.kdl")
}

var niriMods = []struct {
	m    contracts.Modifier
	name string
}{
	{contracts.ModCtrl, "Ctrl"}, {contracts.ModAlt, "Alt"}, {contracts.ModShift, "Shift"}, {contracts.ModSuper, "Super"},
}

func niriChord(h contracts.Hotkey) string {
	var b strings.Builder
	for _, m := range niriMods {
		if h.Mods&m.m != 0 {
			b.WriteString(m.name + "+")
		}
	}
	x, _ := keys.XKBName(h.Key)
	b.WriteString(x)
	return b.String()
}

func niriBlock(req Request) string {
	q := strconv.Quote(req.Exe)
	line := func(h contracts.Hotkey, title, sub string) string {
		return "    " + niriChord(h) + ` repeat=false hotkey-overlay-title=` + strconv.Quote(title) +
			` { spawn ` + q + ` "ctl" ` + strconv.Quote(sub) + `; }` + "\n"
	}
	return block("//", "binds {\n"+
		line(req.Talk, "Companion: talk", "talk-toggle")+
		line(req.Cancel, "Companion: cancel", "cancel")+
		"}\n")
}

// niri modifier names; Mod is Super in a normal session.
var niriModNames = map[string]contracts.Modifier{
	"mod": contracts.ModSuper, "super": contracts.ModSuper, "win": contracts.ModSuper,
	"ctrl": contracts.ModCtrl, "control": contracts.ModCtrl,
	"alt": contracts.ModAlt, "shift": contracts.ModShift,
}

func applyNiri(ctx context.Context, env Env, req Request) (Result, error) {
	path := niriPath(env)
	blk := niriBlock(req)
	ed, err := prepare(path, "//", blk, false)
	if err != nil {
		return Result{Path: path}, err
	}
	res := Result{Path: path, Changed: ed.changed()}
	// The block goes last and later binds override earlier ones, so a key
	// the user bound themselves would be taken silently.
	if used := niriBound(path, string(ed.old)); used[req.Talk] || used[req.Cancel] {
		return res, fail(CodeConflict, contracts.ErrHotkeyConflict)
	}
	if ed.changed() && env.has("niri") {
		tmp, err := scratch(path, ed.new)
		if err != nil {
			return res, err
		}
		_, verr := env.Run(ctx, "niri", "validate", "-c", tmp)
		os.Remove(tmp)
		if verr != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			return res, fail(CodeInvalid, verr)
		}
	}
	if err := ed.commit(); err != nil {
		return res, err
	}
	return res, niriReload(ctx, env)
}

// niriReload asks the running niri to load the file now (it would also
// notice the change itself) and checks the outcome it reports.
func niriReload(ctx context.Context, env Env) error {
	if env.getenv("NIRI_SOCKET") == "" || !env.has("niri") {
		return nil // not running in niri: the file is picked up at login
	}
	if _, err := env.Run(ctx, "niri", "msg", "action", "load-config-file"); err != nil {
		return fail(CodeReloadFailed, err)
	}
	// The event stream starts with the current state, including the last
	// load's outcome; it never ends by itself.
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := env.Run(sctx, "niri", "msg", "--json", "event-stream")
	failed, seen := niriLoadFailed(out)
	switch {
	case seen && failed:
		return fail(CodeReloadFailed, errors.New("niri reports the config failed to load"))
	case seen:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil && sctx.Err() == nil:
		return fail(CodeReloadFailed, err)
	}
	return nil // older niri without the event: validate already passed
}

func niriLoadFailed(out []byte) (failed, seen bool) {
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var ev struct {
			ConfigLoaded *struct {
				Failed bool `json:"failed"`
			} `json:"ConfigLoaded"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.ConfigLoaded != nil {
			return ev.ConfigLoaded.Failed, true
		}
	}
	return false, false
}

// niriBound collects the chords bound in content (the main config without
// our block) and in the files it includes.
func niriBound(path, content string) map[contracts.Hotkey]bool {
	used := map[contracts.Hotkey]bool{}
	seen := map[string]bool{}
	var scan func(dir, content string, depth int)
	scan = func(dir, content string, depth int) {
		for _, inc := range kdlBinds(withoutBlock(content, "//"), used) {
			if depth > 8 {
				return
			}
			p := inc
			if !filepath.IsAbs(p) {
				p = filepath.Join(dir, p)
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			if b, err := os.ReadFile(p); err == nil {
				scan(filepath.Dir(p), string(b), depth+1)
			}
		}
	}
	scan(filepath.Dir(path), content, 0)
	return used
}

// kdlBinds adds the key chords of every binds block to used and returns
// the include paths. niri binds are one node per line, so a line scan
// with brace tracking is enough; braces in strings are skipped.
func kdlBinds(content string, used map[contracts.Hotkey]bool) (includes []string) {
	var stack []string
	for _, raw := range strings.Split(content, "\n") {
		line, opens, closes := kdlLine(raw)
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(stack) > 0 && stack[len(stack)-1] == "binds" {
			name := strings.TrimSuffix(f[0], "{")
			if h, ok := parseForeign(name, niriModNames); ok {
				used[h] = true
			}
		}
		if len(stack) == 0 && f[0] == "include" && len(f) > 1 {
			if p, err := strconv.Unquote(f[1]); err == nil {
				includes = append(includes, p)
			}
		}
		// A node that opens and closes its children on one line (a bind)
		// leaves the stack as it was.
		for range max(0, opens-closes) {
			stack = append(stack, strings.TrimSuffix(f[0], "{"))
		}
		for range max(0, closes-opens) {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return includes
}

// kdlLine strips a // comment and counts braces outside strings.
func kdlLine(s string) (line string, opens, closes int) {
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr && esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case !inStr && c == '/' && i+1 < len(s) && s[i+1] == '/':
			return s[:i], opens, closes
		case !inStr && c == '{':
			opens++
		case !inStr && c == '}':
			closes++
		}
	}
	return s, opens, closes
}
