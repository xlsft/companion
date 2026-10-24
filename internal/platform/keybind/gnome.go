package keybind

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/xlsft/companion/internal/platform/keys"
	"github.com/xlsft/companion/pkg/contracts"
)

// GNOME without the portal: custom keybindings of gnome-settings-daemon,
// set through gsettings. They fire on press only. The list of custom
// bindings is shared with the user's own, so ours are added to it, never
// written over it.

const (
	gnomeSchema    = "org.gnome.settings-daemon.plugins.media-keys"
	gnomeKey       = "custom-keybindings"
	gnomeRelSchema = "org.gnome.settings-daemon.plugins.media-keys.custom-keybinding"
	gnomeBase      = "/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/"
)

type gnomeBind struct {
	path, name, command, binding string
}

func gnomeBinds(req Request) []gnomeBind {
	talk, cancel := Commands(req.Exe)
	b := []gnomeBind{
		{gnomeBase + "companion-talk/", "Companion: talk", talk, gnomeChord(req.Talk)},
		{gnomeBase + "companion-cancel/", "Companion: cancel", cancel, gnomeChord(req.Cancel)},
	}
	if req.Dictate.Key != "" {
		b = append(b, gnomeBind{gnomeBase + "companion-dictate/", "Companion: dictate", DictateCommand(req.Exe), gnomeChord(req.Dictate)})
	}
	return b
}

var gnomeMods = []struct {
	m    contracts.Modifier
	name string
}{
	{contracts.ModCtrl, "<Control>"}, {contracts.ModAlt, "<Alt>"}, {contracts.ModShift, "<Shift>"}, {contracts.ModSuper, "<Super>"},
}

func gnomeChord(h contracts.Hotkey) string {
	var b strings.Builder
	for _, m := range gnomeMods {
		if h.Mods&m.m != 0 {
			b.WriteString(m.name)
		}
	}
	x, _ := keys.XKBName(h.Key)
	b.WriteString(x)
	return b.String()
}

// gvString quotes s as a GVariant text-format string.
func gvString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// parseStrv reads a GVariant string array as gsettings prints it:
// "@as []" or "['/a/', '/b/']".
func parseStrv(s string) ([]string, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "@as"))
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, errors.New("not a string array")
	}
	s = s[1 : len(s)-1]
	var out []string
	for {
		s = strings.TrimLeft(s, " ,")
		if s == "" {
			return out, nil
		}
		q := s[0]
		if q != '\'' && q != '"' {
			return nil, errors.New("unquoted element")
		}
		var b strings.Builder
		i := 1
		for ; i < len(s) && s[i] != q; i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
			}
			b.WriteByte(s[i])
		}
		if i >= len(s) {
			return nil, errors.New("unterminated string")
		}
		out = append(out, b.String())
		s = s[i+1:]
	}
}

func formatStrv(v []string) string {
	q := make([]string, len(v))
	for i, s := range v {
		q[i] = gvString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func gsGet(ctx context.Context, env Env, schema, key string) (string, error) {
	out, err := env.Run(ctx, "gsettings", "get", schema, key)
	return strings.TrimSpace(string(out)), err
}

func applyGNOME(ctx context.Context, env Env, req Request) (Result, error) {
	if !env.has("gsettings") {
		return Result{}, fail(CodeUnsupported, contracts.Unsupported(contracts.CapGlobalHotkey))
	}
	raw, err := gsGet(ctx, env, gnomeSchema, gnomeKey)
	if err != nil {
		return Result{}, fail(CodeToolFailed, err)
	}
	list, err := parseStrv(raw)
	if err != nil {
		return Result{}, fail(CodeToolFailed, err)
	}
	changed := false
	binds := gnomeBinds(req)
	for _, b := range binds {
		if !slices.Contains(list, b.path) {
			list = append(list, b.path)
			changed = true
		}
	}
	if changed {
		if _, err := env.Run(ctx, "gsettings", "set", gnomeSchema, gnomeKey, formatStrv(list)); err != nil {
			return Result{}, fail(CodeToolFailed, err)
		}
	}
	for _, b := range binds {
		rel := gnomeRelSchema + ":" + b.path
		for _, kv := range [][2]string{{"name", b.name}, {"command", b.command}, {"binding", b.binding}} {
			if cur, err := gsGet(ctx, env, rel, kv[0]); err == nil && cur == gvString(kv[1]) {
				continue
			}
			changed = true
			if _, err := env.Run(ctx, "gsettings", "set", rel, kv[0], gvString(kv[1])); err != nil {
				return Result{}, fail(CodeToolFailed, err)
			}
		}
	}
	if !gnomeApplied(ctx, env, req) {
		return Result{Changed: changed}, fail(CodeReloadFailed, errors.New("bindings did not persist"))
	}
	return Result{Changed: changed}, nil
}

func gnomeApplied(ctx context.Context, env Env, req Request) bool {
	if !env.has("gsettings") {
		return false
	}
	raw, err := gsGet(ctx, env, gnomeSchema, gnomeKey)
	if err != nil {
		return false
	}
	list, err := parseStrv(raw)
	if err != nil {
		return false
	}
	for _, b := range gnomeBinds(req) {
		if !slices.Contains(list, b.path) {
			return false
		}
		rel := gnomeRelSchema + ":" + b.path
		for _, kv := range [][2]string{{"command", b.command}, {"binding", b.binding}} {
			if cur, err := gsGet(ctx, env, rel, kv[0]); err != nil || cur != gvString(kv[1]) {
				return false
			}
		}
	}
	return true
}
