package keybind

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/xlsft/companion/internal/platform/keys"
	"github.com/xlsft/companion/pkg/contracts"
)

// Hyprland's GlobalShortcuts portal registers shortcuts but leaves the
// keys to hyprland.conf, so Hyprland always gets the config block; bindr
// gives key-up, so Talk is hold-to-talk.

func hyprPath(env Env) string {
	return filepath.Join(env.ConfigHome, "hypr", "hyprland.conf")
}

var hyprMods = []struct {
	m    contracts.Modifier
	name string
}{
	{contracts.ModCtrl, "CTRL"}, {contracts.ModAlt, "ALT"}, {contracts.ModShift, "SHIFT"}, {contracts.ModSuper, "SUPER"},
}

func hyprChord(h contracts.Hotkey) (mods, key string) {
	var ms []string
	for _, m := range hyprMods {
		if h.Mods&m.m != 0 {
			ms = append(ms, m.name)
		}
	}
	x, _ := keys.XKBName(h.Key)
	return strings.Join(ms, " "), x
}

func hyprBlock(req Request) string {
	e := shellWord(req.Exe)
	tm, tk := hyprChord(req.Talk)
	cm, ck := hyprChord(req.Cancel)
	s := "bind = " + tm + ", " + tk + ", exec, " + e + " ctl talk-press\n" +
		"bindr = " + tm + ", " + tk + ", exec, " + e + " ctl talk-release\n" +
		"bind = " + cm + ", " + ck + ", exec, " + e + " ctl cancel\n"
	if req.Dictate.Key != "" {
		dm, dk := hyprChord(req.Dictate)
		s += "bind = " + dm + ", " + dk + ", exec, " + e + " ctl dictate-press\n" +
			"bindr = " + dm + ", " + dk + ", exec, " + e + " ctl dictate-release\n"
	}
	// A modifier-only bind takes the modifier's own mask plus r; another
	// bind used meanwhile shadows its release (hyprtester
	// modifierReleaseBindShadowing).
	if req.DoubleTap != "" {
		mod := map[string]string{"ctrl": "CTRL", "shift": "SHIFT", "alt": "ALT", "super": "SUPER"}[req.DoubleTap]
		for _, k := range tapKeys[req.DoubleTap] {
			s += "bindr = " + mod + ", " + k + ", exec, " + TapCommand(req.Exe, req.DoubleTap) + "\n"
		}
	}
	return block("#", s)
}

var hyprModNames = map[string]contracts.Modifier{
	"super": contracts.ModSuper, "win": contracts.ModSuper, "mod4": contracts.ModSuper, "logo": contracts.ModSuper,
	"ctrl": contracts.ModCtrl, "control": contracts.ModCtrl, "alt": contracts.ModAlt, "mod1": contracts.ModAlt,
	"shift": contracts.ModShift,
}

// hyprBound collects press binds ("bind", "binde", ... but not "bindr");
// $variables in the modifier field are resolved from plain assignments.
func hyprBound(content string) map[contracts.Hotkey]bool {
	used := map[contracts.Hotkey]bool{}
	vars := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if strings.HasPrefix(k, "$") {
			vars[k] = v
			continue
		}
		if !strings.HasPrefix(k, "bind") || strings.ContainsRune(strings.TrimPrefix(k, "bind"), 'r') {
			continue
		}
		f := strings.SplitN(v, ",", 3)
		if len(f) < 2 {
			continue
		}
		mods := f[0]
		for name, val := range vars {
			mods = strings.ReplaceAll(mods, name, val)
		}
		chord := strings.Join(append(strings.FieldsFunc(mods, func(r rune) bool { return r == ' ' || r == '_' }), strings.TrimSpace(f[1])), "+")
		if h, ok := parseForeign(chord, hyprModNames); ok {
			used[h] = true
		}
	}
	return used
}

func applyHyprland(ctx context.Context, env Env, req Request) (Result, error) {
	path := hyprPath(env)
	ed, err := prepare(path, "#", hyprBlock(req), false)
	if err != nil {
		return Result{Path: path}, err
	}
	res := Result{Path: path, Changed: ed.changed()}
	if req.takenIn(hyprBound(withoutBlock(string(ed.old), "#"))) {
		return res, fail(CodeConflict, contracts.ErrHotkeyConflict)
	}
	running := env.getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" && env.has("hyprctl")
	before := 0
	if running {
		before = hyprErrors(ctx, env)
	}
	if err := ed.commit(); err != nil {
		return res, err
	}
	if !running {
		return res, nil
	}
	if _, err := env.Run(ctx, "hyprctl", "reload"); err != nil {
		return res, fail(CodeReloadFailed, err)
	}
	// Hyprland has no offline validator; errors that appear with our
	// block are ours, so take it back out.
	if hyprErrors(ctx, env) > before {
		_ = ed.rollback()
		_, _ = env.Run(ctx, "hyprctl", "reload")
		return res, fail(CodeInvalid, nil)
	}
	return res, nil
}

// hyprErrors counts the config errors Hyprland reports.
func hyprErrors(ctx context.Context, env Env) int {
	out, err := env.Run(ctx, "hyprctl", "-j", "configerrors")
	if err != nil {
		return 0
	}
	var list []string
	if json.Unmarshal(out, &list) != nil {
		return 0
	}
	n := 0
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			n++
		}
	}
	return n
}
