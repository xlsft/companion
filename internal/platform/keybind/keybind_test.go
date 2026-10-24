package keybind

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/companion/pkg/contracts"
)

var (
	superSpace     = contracts.Hotkey{Mods: contracts.ModSuper, Key: "space"}
	superBackspace = contracts.Hotkey{Mods: contracts.ModSuper, Key: "backspace"}
	ctrlAltK       = contracts.Hotkey{Mods: contracts.ModCtrl | contracts.ModAlt, Key: "k"}
	req            = Request{Talk: superSpace, Cancel: superBackspace, Exe: "/opt/companion/bin/companion"}
)

func TestResolve(t *testing.T) {
	tests := []struct {
		comp, desktop string
		primary       bool
		want          Method
	}{
		{"", "", true, MethodService}, // X11, Windows, macOS
		{"", "KDE", true, MethodService},
		{"", "GNOME", true, MethodService},
		{"hyprland", "Hyprland", true, MethodHyprland},
		{"hyprland", "Hyprland", false, MethodHyprland},
		{"niri", "niri", false, MethodNiri},
		{"sway", "sway", false, MethodSway},
		{"", "ubuntu:GNOME", false, MethodGNOME},
		{"", "KDE", false, MethodManual},
		{"", "", false, MethodManual},
	}
	for _, tt := range tests {
		if got := Resolve(tt.comp, tt.desktop, tt.primary); got != tt.want {
			t.Errorf("Resolve(%q, %q, %v) = %s, want %s", tt.comp, tt.desktop, tt.primary, got, tt.want)
		}
	}
}

func TestChordConversion(t *testing.T) {
	shiftF5 := contracts.Hotkey{Mods: contracts.ModShift | contracts.ModSuper, Key: "f5"}
	tests := []struct {
		h                 contracts.Hotkey
		niri, sway, gnome string
		hyprMods, hyprKey string
	}{
		{superSpace, "Super+space", "Mod4+space", "<Super>space", "SUPER", "space"},
		{superBackspace, "Super+BackSpace", "Mod4+BackSpace", "<Super>BackSpace", "SUPER", "BackSpace"},
		{ctrlAltK, "Ctrl+Alt+k", "Ctrl+Mod1+k", "<Control><Alt>k", "CTRL ALT", "k"},
		{shiftF5, "Shift+Super+F5", "Shift+Mod4+F5", "<Shift><Super>F5", "SHIFT SUPER", "F5"},
	}
	for _, tt := range tests {
		if got := niriChord(tt.h); got != tt.niri {
			t.Errorf("niri %v = %q, want %q", tt.h, got, tt.niri)
		}
		if got := swayChord(tt.h); got != tt.sway {
			t.Errorf("sway %v = %q, want %q", tt.h, got, tt.sway)
		}
		if got := gnomeChord(tt.h); got != tt.gnome {
			t.Errorf("gnome %v = %q, want %q", tt.h, got, tt.gnome)
		}
		if m, k := hyprChord(tt.h); m != tt.hyprMods || k != tt.hyprKey {
			t.Errorf("hyprland %v = %q %q, want %q %q", tt.h, m, k, tt.hyprMods, tt.hyprKey)
		}
	}
}

func TestUpsert(t *testing.T) {
	blk := block("//", "binds {\n}\n")
	other := block("//", "binds {\n    Mod+K { spawn \"x\"; }\n}\n")
	tests := []struct {
		name, in, want string
		code           string
	}{
		{"empty file", "", blk, ""},
		{"append after newline", "a\n", "a\n\n" + blk, ""},
		{"append without newline", "a", "a\n\n" + blk, ""},
		{"append after blank line", "a\n\n", "a\n\n" + blk, ""},
		{"replace keeps surroundings", "head\n\n" + other + "tail\n", "head\n\n" + blk + "tail\n", ""},
		{"idempotent", "head\n\n" + blk, "head\n\n" + blk, ""},
		{"indented markers", "x\n  // >>> companion\n  old\n  // <<< companion\ny\n", "x\n" + blk + "y\n", ""},
		{"start without end", "x\n// >>> companion\nold\n", "", CodeDamagedBlock},
		{"nested start", "// >>> companion\n// >>> companion\n// <<< companion\n", "", CodeDamagedBlock},
	}
	for _, tt := range tests {
		got, err := upsert(tt.in, "//", blk)
		if ErrorCode(err) != tt.code {
			t.Errorf("%s: err %v, want %q", tt.name, err, tt.code)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
		if err == nil {
			again, _ := upsert(got, "//", blk)
			if again != got {
				t.Errorf("%s: second upsert changed the content", tt.name)
			}
		}
	}
}

// fakeTools answers tool runs by command line.
type fakeTools struct {
	mu    sync.Mutex
	calls []string
	reply func(cmd string, args []string) ([]byte, error)
}

func (f *fakeTools) run(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	f.mu.Unlock()
	if f.reply == nil {
		return nil, nil
	}
	return f.reply(cmd, args)
}

func (f *fakeTools) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func testEnv(t *testing.T, tools *fakeTools, vars map[string]string, installed ...string) Env {
	t.Helper()
	return Env{
		ConfigHome: t.TempDir(),
		Getenv:     func(k string) string { return vars[k] },
		Run:        tools.run,
		LookPath: func(n string) (string, error) {
			if slices.Contains(installed, n) {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const niriUser = `input {
    keyboard { xkb { layout "us"; } }
}
binds {
    Mod+T { spawn "alacritty"; }
    Mod+Shift+Slash { show-hotkey-overlay; }
}
include "extra.kdl"
`

func TestApplyNiri(t *testing.T) {
	niriEvents := func(failed string) func(string, []string) ([]byte, error) {
		return func(cmd string, _ []string) ([]byte, error) {
			if strings.Contains(cmd, "event-stream") {
				return []byte(`{"WorkspacesChanged":{}}` + "\n" + `{"ConfigLoaded":{"failed":` + failed + `}}` + "\n"), context.DeadlineExceeded
			}
			return nil, nil
		}
	}
	running := map[string]string{"NIRI_SOCKET": "/run/niri.sock"}
	tests := []struct {
		name     string
		extra    string // extra.kdl
		reply    func(string, []string) ([]byte, error)
		vars     map[string]string
		req      Request
		code     string
		reloaded bool
	}{
		{name: "fresh", extra: "binds {\n    Mod+A { spawn \"a\"; }\n}\n", reply: niriEvents("false"), vars: running, req: req, reloaded: true},
		{name: "not running: written, no reload", extra: "", req: req},
		{name: "taken in an include", extra: "binds {\n    Mod+Space { spawn \"launcher\"; }\n}\n", req: req, code: CodeConflict},
		{name: "taken in main", req: Request{Talk: contracts.Hotkey{Mods: contracts.ModSuper, Key: "t"}, Cancel: superBackspace, Exe: req.Exe}, code: CodeConflict},
		{name: "validator rejects", reply: func(cmd string, _ []string) ([]byte, error) {
			if strings.HasPrefix(cmd, "niri validate") {
				return nil, errors.New("exit status 1")
			}
			return nil, nil
		}, req: req, code: CodeInvalid},
		{name: "reload failed", reply: niriEvents("true"), vars: running, req: req, code: CodeReloadFailed, reloaded: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := &fakeTools{reply: tt.reply}
			env := testEnv(t, tools, tt.vars, "niri")
			path := filepath.Join(env.ConfigHome, "niri", "config.kdl")
			write(t, path, niriUser)
			write(t, filepath.Join(env.ConfigHome, "niri", "extra.kdl"), tt.extra)
			_, err := Apply(context.Background(), MethodNiri, env, tt.req)
			if ErrorCode(err) != tt.code {
				t.Fatalf("err %v, want %q", err, tt.code)
			}
			if tools.called("niri msg action load-config-file") != tt.reloaded {
				t.Errorf("reload = %v, want %v (%v)", !tt.reloaded, tt.reloaded, tools.calls)
			}
			got := read(t, path)
			_, bakErr := os.Stat(path + BackupSuffix)
			if tt.code != "" && tt.code != CodeReloadFailed {
				if got != niriUser {
					t.Error("rejected apply changed the config")
				}
				if bakErr == nil {
					t.Error("backup made for an apply that wrote nothing")
				}
				return
			}
			if !strings.HasPrefix(got, niriUser) {
				t.Errorf("user content not kept:\n%s", got)
			}
			for _, want := range []string{
				`Super+space repeat=false hotkey-overlay-title="Companion: talk" { spawn "/opt/companion/bin/companion" "ctl" "talk-toggle"; }`,
				`Super+BackSpace repeat=false hotkey-overlay-title="Companion: cancel" { spawn "/opt/companion/bin/companion" "ctl" "cancel"; }`,
				"// >>> companion", "// <<< companion",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("config lacks %q:\n%s", want, got)
				}
			}
			if bak := read(t, path+BackupSuffix); bak != niriUser {
				t.Errorf("backup is not the original:\n%s", bak)
			}
			if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
				t.Errorf("mode %v, want 0640", st.Mode().Perm())
			}
			if !Applied(context.Background(), MethodNiri, env, tt.req) {
				t.Error("Applied false after apply")
			}
			if tt.code == "" && !tools.called("niri validate -c "+filepath.Join(env.ConfigHome, "niri", ".config.kdl.companion-")) {
				t.Errorf("not validated before writing: %v", tools.calls)
			}
			left, _ := filepath.Glob(filepath.Join(env.ConfigHome, "niri", ".*companion*"))
			if len(left) > 0 {
				t.Errorf("temporary files left: %v", left)
			}
		})
	}
}

func TestApplyNiriReapply(t *testing.T) {
	tools := &fakeTools{}
	env := testEnv(t, tools, nil, "niri")
	path := filepath.Join(env.ConfigHome, "niri", "config.kdl")
	write(t, path, niriUser)
	ctx := context.Background()
	if _, err := Apply(ctx, MethodNiri, env, req); err != nil {
		t.Fatal(err)
	}
	// The user edits around the block afterwards.
	write(t, path, "// mine\n"+read(t, path)+"window-rule {}\n")
	r2 := Request{Talk: ctrlAltK, Cancel: superBackspace, Exe: req.Exe}
	res, err := Apply(ctx, MethodNiri, env, r2)
	if err != nil || !res.Changed {
		t.Fatalf("reapply: %v changed=%v", err, res.Changed)
	}
	got := read(t, path)
	if n := strings.Count(got, ">>> companion"); n != 1 {
		t.Errorf("%d blocks after reapply:\n%s", n, got)
	}
	if strings.Contains(got, "Super+space") || !strings.Contains(got, "Ctrl+Alt+k") {
		t.Errorf("block not replaced:\n%s", got)
	}
	if !strings.HasPrefix(got, "// mine\n"+niriUser) || !strings.HasSuffix(got, "// <<< companion\nwindow-rule {}\n") {
		t.Errorf("user edits lost:\n%s", got)
	}
	if read(t, path+BackupSuffix) != niriUser {
		t.Error("backup overwritten by the second apply")
	}
	if Applied(ctx, MethodNiri, env, req) {
		t.Error("old chords still reported as applied")
	}
	res, err = Apply(ctx, MethodNiri, env, r2)
	if err != nil || res.Changed {
		t.Errorf("idempotent apply: %v changed=%v", err, res.Changed)
	}
}

func TestApplyNiriSymlinkAndMissing(t *testing.T) {
	tools := &fakeTools{}
	env := testEnv(t, tools, nil)
	ctx := context.Background()
	if _, err := Apply(ctx, MethodNiri, env, req); ErrorCode(err) != CodeConfigMissing {
		t.Fatalf("missing config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.ConfigHome, "niri", "config.kdl")); err == nil {
		t.Fatal("a missing config was created")
	}
	dots := filepath.Join(t.TempDir(), "dotfiles", "config.kdl")
	write(t, dots, niriUser)
	link := filepath.Join(env.ConfigHome, "niri", "config.kdl")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dots, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, MethodNiri, env, req); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced by a file")
	}
	if !strings.Contains(read(t, dots), ">>> companion") {
		t.Error("target not written")
	}
	if read(t, dots+BackupSuffix) != niriUser {
		t.Error("backup not next to the target")
	}
}

func TestApplyNiriHonoursNiriConfig(t *testing.T) {
	tools := &fakeTools{}
	custom := filepath.Join(t.TempDir(), "my.kdl")
	write(t, custom, niriUser)
	env := testEnv(t, tools, map[string]string{"NIRI_CONFIG": custom})
	res, err := Apply(context.Background(), MethodNiri, env, req)
	if err != nil || res.Path != custom {
		t.Fatalf("%v %q", err, res.Path)
	}
}

func TestKDLBinds(t *testing.T) {
	src := `binds {
    Mod+Q { close-window; }
    Super+Shift+E { quit; } // comment Mod+W
    Mod+WheelScrollDown { focus-workspace-down; }
    Ctrl+Alt+Delete { spawn "x" "{" ; }
    Mod+Return hotkey-overlay-title="Terminal" { spawn "foot"; }
}
// Mod+Z { nope; }
window-rule { match app-id="Mod+Y"; }
recent-windows {
    binds {
        Alt+Tab { next-window; }
    }
}
include "a.kdl"
`
	used := map[contracts.Hotkey]bool{}
	inc := kdlBinds(src, used)
	want := []contracts.Hotkey{
		{Mods: contracts.ModSuper, Key: "q"},
		{Mods: contracts.ModSuper | contracts.ModShift, Key: "e"},
		{Mods: contracts.ModCtrl | contracts.ModAlt, Key: "delete"},
		{Mods: contracts.ModSuper, Key: "return"},
		{Mods: contracts.ModAlt, Key: "tab"},
	}
	for _, h := range want {
		if !used[h] {
			t.Errorf("missing %v", h)
		}
	}
	if len(used) != len(want) {
		t.Errorf("used = %v", used)
	}
	if !slices.Equal(inc, []string{"a.kdl"}) {
		t.Errorf("includes = %v", inc)
	}
}

func TestApplySway(t *testing.T) {
	tests := []struct {
		name    string
		main    string
		target  string // relative to sway dir
		reply   func(string, []string) ([]byte, error)
		code    string
		rolled  bool
		reload  bool
		vars    map[string]string
		mainOut string // expected main config after, "" = unchanged
	}{
		{name: "drop-in", main: "set $mod Mod4\ninclude ~/.config/sway/config.d/*\n", target: "config.d/companion.conf",
			vars: map[string]string{"SWAYSOCK": "/run/sway.sock"}, reload: true},
		{name: "drop-in by $HOME and .conf glob", main: "include $HOME/.config/sway/config.d/*.conf\n", target: "config.d/companion.conf"},
		{name: "block in main", main: "set $mod Mod4\nbindsym $mod+Return exec foot\n", target: "config"},
		{name: "system include only", main: "include /etc/sway/config.d/*\n", target: "config"},
		{name: "conflict through $mod", main: "set $mod Mod4\nbindsym $mod+space focus mode_toggle\n", target: "config", code: CodeConflict},
		{name: "release bind is no conflict", main: "bindsym --release Mod4+BackSpace exec x\n", target: "config"},
		{name: "validator rejects: rolled back", main: "include ~/.config/sway/config.d/*\n", target: "config.d/companion.conf",
			reply: func(cmd string, _ []string) ([]byte, error) {
				if strings.HasPrefix(cmd, "sway -C") {
					return nil, errors.New("exit status 1")
				}
				return nil, nil
			}, code: CodeInvalid, rolled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := &fakeTools{reply: tt.reply}
			env := testEnv(t, tools, tt.vars, "sway", "swaymsg")
			home := filepath.Dir(env.ConfigHome)
			env.Getenv = func(k string) string {
				if k == "HOME" {
					return home
				}
				return tt.vars[k]
			}
			// ConfigHome must be ~/.config for the ~ includes to match.
			env.ConfigHome = filepath.Join(home, ".config")
			dir := filepath.Join(env.ConfigHome, "sway")
			main := filepath.Join(dir, "config")
			write(t, main, tt.main)
			res, err := Apply(context.Background(), MethodSway, env, req)
			if ErrorCode(err) != tt.code {
				t.Fatalf("err %v, want %q", err, tt.code)
			}
			if want := filepath.Join(dir, tt.target); res.Path != want {
				t.Errorf("target %q, want %q", res.Path, want)
			}
			if tools.called("swaymsg reload") != tt.reload {
				t.Errorf("reload: %v", tools.calls)
			}
			got, rerr := os.ReadFile(res.Path)
			switch {
			case tt.code == CodeConflict:
				if read(t, main) != tt.main {
					t.Error("conflict changed the config")
				}
				return
			case tt.rolled:
				if rerr == nil && strings.Contains(string(got), "companion") {
					t.Errorf("rejected block left behind:\n%s", got)
				}
				return
			}
			for _, want := range []string{
				"bindsym Mod4+space exec /opt/companion/bin/companion ctl talk-press",
				"bindsym --release Mod4+space exec /opt/companion/bin/companion ctl talk-release",
				"bindsym Mod4+BackSpace exec /opt/companion/bin/companion ctl cancel",
			} {
				if !strings.Contains(string(got), want) {
					t.Errorf("lacks %q:\n%s", want, got)
				}
			}
			if tt.target != "config" && read(t, main) != tt.main {
				t.Error("main config touched despite the drop-in")
			}
			if !Applied(context.Background(), MethodSway, env, req) {
				t.Error("Applied false")
			}
		})
	}
}

func TestApplyHyprland(t *testing.T) {
	const user = "$mainMod = SUPER\nbind = $mainMod, Q, exec, kitty\n"
	tests := []struct {
		name   string
		user   string
		errs   []string // configerrors after our reload
		code   string
		inFile bool
	}{
		{name: "fresh", user: user, inFile: true},
		{name: "conflict via variable", user: "$mainMod = SUPER\nbind = $mainMod, SPACE, togglefloating\n", code: CodeConflict},
		{name: "new errors roll back", user: user, errs: []string{"config error at line 9"}, code: CodeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reloads := 0
			tools := &fakeTools{reply: func(cmd string, _ []string) ([]byte, error) {
				switch cmd {
				case "hyprctl reload":
					reloads++
					return []byte("ok"), nil
				case "hyprctl -j configerrors":
					if reloads == 1 && len(tt.errs) > 0 {
						return []byte(`["` + strings.Join(tt.errs, `","`) + `"]`), nil
					}
					return []byte(`[""]`), nil
				}
				return nil, nil
			}}
			env := testEnv(t, tools, map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": "x"}, "hyprctl")
			path := filepath.Join(env.ConfigHome, "hypr", "hyprland.conf")
			write(t, path, tt.user)
			_, err := Apply(context.Background(), MethodHyprland, env, req)
			if ErrorCode(err) != tt.code {
				t.Fatalf("err %v, want %q", err, tt.code)
			}
			got := read(t, path)
			if !tt.inFile {
				if got != tt.user {
					t.Errorf("config changed:\n%s", got)
				}
				return
			}
			for _, want := range []string{
				"bind = SUPER, space, exec, /opt/companion/bin/companion ctl talk-press",
				"bindr = SUPER, space, exec, /opt/companion/bin/companion ctl talk-release",
				"bind = SUPER, BackSpace, exec, /opt/companion/bin/companion ctl cancel",
				"# >>> companion",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("lacks %q:\n%s", want, got)
				}
			}
		})
	}
}

// fakeGSettings keeps gsettings keys in memory.
type fakeGSettings struct {
	mu   sync.Mutex
	vals map[string]string
}

func (g *fakeGSettings) reply(_ string, args []string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := args[1] + " " + args[2]
	switch args[0] {
	case "get":
		v, ok := g.vals[k]
		if !ok {
			if args[2] == gnomeKey {
				return []byte("@as []\n"), nil
			}
			return []byte("''\n"), nil
		}
		return []byte(v + "\n"), nil
	case "set":
		g.vals[k] = args[3]
	}
	return nil, nil
}

func TestApplyGNOME(t *testing.T) {
	g := &fakeGSettings{vals: map[string]string{gnomeSchema + " " + gnomeKey: "['/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/custom0/']"}}
	tools := &fakeTools{reply: g.reply}
	env := testEnv(t, tools, nil, "gsettings")
	ctx := context.Background()
	if Applied(ctx, MethodGNOME, env, req) {
		t.Fatal("applied before apply")
	}
	res, err := Apply(ctx, MethodGNOME, env, req)
	if err != nil || !res.Changed {
		t.Fatalf("%v changed=%v", err, res.Changed)
	}
	list, _ := parseStrv(g.vals[gnomeSchema+" "+gnomeKey])
	want := []string{gnomeBase + "custom0/", gnomeBase + "companion-talk/", gnomeBase + "companion-cancel/"}
	if !slices.Equal(list, want) {
		t.Errorf("list = %v, want %v", list, want)
	}
	rel := gnomeRelSchema + ":" + gnomeBase + "companion-talk/"
	if got := g.vals[rel+" binding"]; got != "'<Super>space'" {
		t.Errorf("binding = %s", got)
	}
	if got := g.vals[rel+" command"]; got != "'/opt/companion/bin/companion ctl talk-toggle'" {
		t.Errorf("command = %s", got)
	}
	res, err = Apply(ctx, MethodGNOME, env, req)
	if err != nil || res.Changed {
		t.Errorf("second apply: %v changed=%v", err, res.Changed)
	}
	if !Applied(ctx, MethodGNOME, env, req) {
		t.Error("Applied false")
	}
}

func TestParseStrv(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		bad  bool
	}{
		{"@as []", nil, false},
		{"[]", nil, false},
		{"['/a/', '/b/']", []string{"/a/", "/b/"}, false},
		{`["it's"]`, []string{"it's"}, false},
		{`['a\'b']`, []string{"a'b"}, false},
		{"['a'", nil, true},
		{"[a]", nil, true},
	}
	for _, tt := range tests {
		got, err := parseStrv(tt.in)
		if (err != nil) != tt.bad || !slices.Equal(got, tt.want) {
			t.Errorf("parseStrv(%q) = %v, %v", tt.in, got, err)
		}
	}
	if got := formatStrv([]string{"/a/", "it's"}); got != `['/a/', 'it\'s']` {
		t.Errorf("formatStrv = %s", got)
	}
}

func TestManualAndService(t *testing.T) {
	env := testEnv(t, &fakeTools{}, nil)
	if _, err := Apply(context.Background(), MethodManual, env, req); !errors.Is(err, contracts.ErrUnsupported) {
		t.Errorf("manual: %v", err)
	}
	if _, err := Apply(context.Background(), MethodService, env, req); err != nil {
		t.Errorf("service: %v", err)
	}
	talk, cancel := Commands("/My Apps/companion")
	if talk != `'/My Apps/companion' ctl talk-toggle` || cancel != `'/My Apps/companion' ctl cancel` {
		t.Errorf("Commands = %q %q", talk, cancel)
	}
}

// The dictation chord rides along in every desktop's block, with key-up
// binds where the desktop has them, and counts for conflicts.
func TestDictateBinds(t *testing.T) {
	dreq := req
	dreq.Dictate = ctrlAltK
	e := "/opt/companion/bin/companion"
	tests := []struct {
		name  string
		block string
		want  []string
	}{
		{"niri", niriBlock(dreq), []string{`Ctrl+Alt+k repeat=false hotkey-overlay-title="Companion: dictate" { spawn "` + e + `" "ctl" "dictate-toggle"; }`}},
		{"sway", swayBlock(dreq), []string{"bindsym Ctrl+Mod1+k exec " + e + " ctl dictate-press", "bindsym --release Ctrl+Mod1+k exec " + e + " ctl dictate-release"}},
		{"hyprland", hyprBlock(dreq), []string{"bind = CTRL ALT, k, exec, " + e + " ctl dictate-press", "bindr = CTRL ALT, k, exec, " + e + " ctl dictate-release"}},
	}
	for _, tt := range tests {
		for _, w := range tt.want {
			if !strings.Contains(tt.block, w) {
				t.Errorf("%s block lacks %q:\n%s", tt.name, w, tt.block)
			}
		}
		if strings.Contains(niriBlock(req)+swayBlock(req)+hyprBlock(req), "dictate") {
			t.Errorf("%s: a request without a dictation chord binds one", tt.name)
		}
	}
	if b := gnomeBinds(dreq); len(b) != 3 || b[2].command != e+" ctl dictate-toggle" || b[2].binding != "<Control><Alt>k" {
		t.Errorf("gnome binds = %+v", b)
	}
	if got := DictateCommand("/opt/my app/companion"); got != `'/opt/my app/companion' ctl dictate-toggle` {
		t.Errorf("DictateCommand = %q", got)
	}

	tools := &fakeTools{}
	env := testEnv(t, tools, nil)
	path := filepath.Join(env.ConfigHome, "niri", "config.kdl")
	write(t, path, niriUser)
	write(t, filepath.Join(env.ConfigHome, "niri", "extra.kdl"), "binds {\n    Ctrl+Alt+K { spawn \"x\"; }\n}\n")
	if _, err := Apply(context.Background(), MethodNiri, env, dreq); ErrorCode(err) != CodeConflict {
		t.Errorf("taken dictation chord: %v", err)
	}
	bad := dreq
	bad.Dictate = contracts.Hotkey{Mods: contracts.ModCtrl, Key: "nope"}
	if _, err := Apply(context.Background(), MethodNiri, env, bad); ErrorCode(err) != CodeBadChord {
		t.Errorf("bad dictation chord: %v", err)
	}
}
