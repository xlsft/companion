package keybind

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/companion/internal/platform/keys"
	"github.com/xlsft/companion/pkg/contracts"
)

// Sway binds key-up too, so Talk is real hold-to-talk there.

func swayMain(env Env) string {
	return filepath.Join(env.ConfigHome, "sway", "config")
}

// swayTarget is the file to write: a drop-in when the main config
// includes one at our name, else the main config itself.
func swayTarget(env Env) (path string, dropin bool) {
	main := swayMain(env)
	b, err := os.ReadFile(main)
	if err != nil {
		return main, false
	}
	dir := filepath.Dir(main)
	cands := []string{filepath.Join(dir, "config.d", "companion.conf"), filepath.Join(dir, "config.d", "companion")}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || f[0] != "include" {
			continue
		}
		pat := expandSway(f[1], dir, env)
		for _, c := range cands {
			if ok, _ := filepath.Match(pat, c); ok {
				return c, true
			}
		}
	}
	return main, false
}

// expandSway resolves the variables and relative paths sway's include
// accepts (it uses wordexp).
func expandSway(p, dir string, env Env) string {
	p = strings.Trim(p, `"'`)
	home := filepath.Dir(env.ConfigHome)
	if h := env.getenv("HOME"); h != "" {
		home = h
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		p = filepath.Join(home, rest)
	}
	p = os.Expand(p, func(k string) string {
		switch k {
		case "HOME":
			return home
		case "XDG_CONFIG_HOME":
			return env.ConfigHome
		}
		return env.getenv(k)
	})
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p)
}

var swayMods = []struct {
	m    contracts.Modifier
	name string
}{
	{contracts.ModCtrl, "Ctrl"}, {contracts.ModAlt, "Mod1"}, {contracts.ModShift, "Shift"}, {contracts.ModSuper, "Mod4"},
}

func swayChord(h contracts.Hotkey) string {
	var b strings.Builder
	for _, m := range swayMods {
		if h.Mods&m.m != 0 {
			b.WriteString(m.name + "+")
		}
	}
	x, _ := keys.XKBName(h.Key)
	b.WriteString(x)
	return b.String()
}

func swayBlock(req Request) string {
	e := shellWord(req.Exe)
	t, c := swayChord(req.Talk), swayChord(req.Cancel)
	s := "bindsym " + t + " exec " + e + " ctl talk-press\n" +
		"bindsym --release " + t + " exec " + e + " ctl talk-release\n" +
		"bindsym " + c + " exec " + e + " ctl cancel\n"
	if req.Dictate.Key != "" {
		d := swayChord(req.Dictate)
		s += "bindsym " + d + " exec " + e + " ctl dictate-press\n" +
			"bindsym --release " + d + " exec " + e + " ctl dictate-release\n"
	}
	return block("#", s)
}

var swayModNames = map[string]contracts.Modifier{
	"mod4": contracts.ModSuper, "super": contracts.ModSuper, "ctrl": contracts.ModCtrl, "control": contracts.ModCtrl,
	"mod1": contracts.ModAlt, "alt": contracts.ModAlt, "shift": contracts.ModShift,
}

// swayBound collects bindsym chords; $mod is resolved from a plain
// "set $mod" line.
func swayBound(content string) map[contracts.Hotkey]bool {
	used := map[contracts.Hotkey]bool{}
	vars := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "set" && strings.HasPrefix(f[1], "$") {
			vars[f[1]] = f[2]
			continue
		}
		if len(f) < 2 || f[0] != "bindsym" {
			continue
		}
		i := 1
		for i < len(f) && strings.HasPrefix(f[i], "--") {
			if f[i] == "--release" {
				i = len(f) // a release bind does not take the press
			}
			i++
		}
		if i >= len(f) {
			continue
		}
		chord := f[i]
		for k, v := range vars {
			chord = strings.ReplaceAll(chord, k, v)
		}
		if h, ok := parseForeign(chord, swayModNames); ok {
			used[h] = true
		}
	}
	return used
}

func applySway(ctx context.Context, env Env, req Request) (Result, error) {
	path, dropin := swayTarget(env)
	ed, err := prepare(path, "#", swayBlock(req), dropin)
	if err != nil {
		return Result{Path: path}, err
	}
	res := Result{Path: path, Changed: ed.changed()}
	main, _ := os.ReadFile(swayMain(env))
	if req.takenIn(swayBound(withoutBlock(string(main), "#"))) {
		return res, fail(CodeConflict, contracts.ErrHotkeyConflict)
	}
	if err := ed.commit(); err != nil {
		return res, err
	}
	if ed.changed() && env.has("sway") {
		// sway -C checks the whole config including drop-ins and exits.
		if _, err := env.Run(ctx, "sway", "-C", "-c", swayMain(env)); err != nil {
			_ = ed.rollback()
			return res, fail(CodeInvalid, err)
		}
	}
	if env.getenv("SWAYSOCK") == "" || !env.has("swaymsg") {
		return res, nil
	}
	if _, err := env.Run(ctx, "swaymsg", "reload"); err != nil {
		return res, fail(CodeReloadFailed, err)
	}
	return res, nil
}
