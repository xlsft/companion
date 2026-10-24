package control

import (
	"strconv"
	"strings"
)

// Instructions returns a config snippet that binds Talk and Escape to
// "companion ctl" in the given compositor ("niri", "sway", "hyprland", as
// reported by detect), ready to paste; the setup wizard (OC-70) shows it.
// exe is the command to run, usually the absolute path of the binary;
// empty means "companion" from $PATH. Unknown compositors get the bare
// commands to bind by hand.
//
// niri binds fire on press only, so niri gets toggle mode; they also repeat
// while held unless repeat=false, which would toggle on and off. Sway and
// Hyprland can bind key-up as well; their snippets use press/release for
// hold-to-talk, which also switches the core out of toggle mode.
func Instructions(compositor, exe string) string {
	if exe == "" {
		exe = "companion"
	}
	switch compositor {
	case "niri":
		q := strconv.Quote(exe)
		return `// ~/.config/niri/config.kdl
binds {
    Mod+Space repeat=false { spawn ` + q + ` "ctl" "talk-toggle"; }
    Mod+Escape repeat=false { spawn ` + q + ` "ctl" "cancel"; }
}
`
	case "sway":
		e := shellWord(exe)
		return `# ~/.config/sway/config
# Hold to talk: press starts, release stops.
bindsym $mod+space exec ` + e + ` ctl talk-press
bindsym --release $mod+space exec ` + e + ` ctl talk-release
bindsym $mod+Escape exec ` + e + ` ctl cancel
# Or toggle instead (press once to start, again to stop):
# bindsym $mod+space exec ` + e + ` ctl talk-toggle
`
	case "hyprland":
		e := shellWord(exe)
		return `# ~/.config/hypr/hyprland.conf
# Hold to talk: bind fires on press, bindr on release.
bind = SUPER, SPACE, exec, ` + e + ` ctl talk-press
bindr = SUPER, SPACE, exec, ` + e + ` ctl talk-release
bind = SUPER, ESCAPE, exec, ` + e + ` ctl cancel
# Or toggle instead:
# bind = SUPER, SPACE, exec, ` + e + ` ctl talk-toggle
`
	}
	e := shellWord(exe)
	return `# Bind these commands to keys in your desktop's keyboard settings:
#   Talk (toggle): ` + e + ` ctl talk-toggle
#   Cancel:        ` + e + ` ctl cancel
# If your desktop can run a command on key release, use
#   ` + e + ` ctl talk-press / ` + e + ` ctl talk-release
# for hold-to-talk instead.
`
}

// shellWord quotes s for sh-like exec lines when it has special characters.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
