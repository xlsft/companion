package hotkeys

import (
	"fmt"

	"github.com/godbus/dbus/v5"

	"github.com/xlsft/companion/internal/platform/keys"
	"github.com/xlsft/companion/pkg/contracts"
)

// shortcut is one element of the a(sa{sv}) array BindShortcuts takes.
type shortcut struct {
	ID   string
	Opts map[string]dbus.Variant
}

// shortcutsArg builds the BindShortcuts argument. preferred_trigger is only
// a hint: the backend or the user may pick another chord.
func shortcutsArg(bindings []contracts.HotkeyBinding) ([]shortcut, error) {
	out := make([]shortcut, 0, len(bindings))
	for _, b := range bindings {
		trig, err := keys.PortalTrigger(b.Hotkey)
		if err != nil {
			return nil, fmt.Errorf("hotkey %s: %w", b.ID, err)
		}
		desc := b.Description
		if desc == "" {
			desc = string(b.ID)
		}
		out = append(out, shortcut{ID: string(b.ID), Opts: map[string]dbus.Variant{
			"description":       dbus.MakeVariant(desc),
			"preferred_trigger": dbus.MakeVariant(trig),
		}})
	}
	return out, nil
}

// shortcutInfo is what the portal reports per bound shortcut.
type shortcutInfo struct {
	ID          string
	Description string
	Trigger     string // trigger_description, human-readable
}

// decodeShortcuts reads an a(sa{sv}) value as godbus delivers it: inside a
// variant it arrives as [][]any, in a signal body typed structs are not
// available either, so both shapes are accepted.
func decodeShortcuts(v any) []shortcutInfo {
	if vv, ok := v.(dbus.Variant); ok {
		v = vv.Value()
	}
	var out []shortcutInfo
	add := func(id string, opts map[string]dbus.Variant) {
		info := shortcutInfo{ID: id}
		if d, ok := opts["description"].Value().(string); ok {
			info.Description = d
		}
		if d, ok := opts["trigger_description"].Value().(string); ok {
			info.Trigger = d
		}
		out = append(out, info)
	}
	switch arr := v.(type) {
	case [][]any:
		for _, el := range arr {
			if len(el) != 2 {
				continue
			}
			id, ok1 := el[0].(string)
			opts, ok2 := el[1].(map[string]dbus.Variant)
			if ok1 && ok2 {
				add(id, opts)
			}
		}
	case []shortcut:
		for _, s := range arr {
			add(s.ID, s.Opts)
		}
	}
	return out
}

// granted maps the portal's answer back onto the requested bindings. Only
// shortcuts the portal reports are returned; the chord is replaced by the
// one the system describes when that description parses.
func granted(requested []contracts.HotkeyBinding, infos []shortcutInfo) []contracts.HotkeyBinding {
	byID := map[string]shortcutInfo{}
	for _, in := range infos {
		byID[in.ID] = in
	}
	var out []contracts.HotkeyBinding
	for _, b := range requested {
		in, ok := byID[string(b.ID)]
		if !ok {
			continue
		}
		b.TriggerDescription = in.Trigger
		if h, err := keys.Parse(in.Trigger); err == nil {
			b.Hotkey = h
		}
		out = append(out, b)
	}
	return out
}

// activation decodes the body of Activated/Deactivated:
// (o session_handle, s shortcut_id, t timestamp, a{sv} options).
func activation(body []any) (session dbus.ObjectPath, id string, ok bool) {
	if len(body) < 2 {
		return "", "", false
	}
	session, ok1 := body[0].(dbus.ObjectPath)
	id, ok2 := body[1].(string)
	return session, id, ok1 && ok2
}

// shortcutsChanged decodes ShortcutsChanged: (o session_handle, a(sa{sv})).
func shortcutsChanged(body []any) (dbus.ObjectPath, []shortcutInfo, bool) {
	if len(body) < 2 {
		return "", nil, false
	}
	session, ok := body[0].(dbus.ObjectPath)
	if !ok {
		return "", nil, false
	}
	return session, decodeShortcuts(body[1]), true
}
