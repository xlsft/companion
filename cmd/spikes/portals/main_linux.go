//go:build linux

// Command portals is the OC-6 spike: it exercises the xdg-desktop-portal
// GlobalShortcuts, ScreenCast (+PipeWire), Screenshot and RemoteDesktop
// interfaces and prints timings. Every subcommand except info and pwgrab
// may show a system consent dialog.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/xlsft/companion/internal/platform/linuxwayland/pipewire"
	"github.com/xlsft/companion/internal/platform/linuxwayland/portal"
)

const usage = `usage: portals <command> [flags]

commands:
  info           portal versions, properties and backend config (no prompts)
  shortcuts      GlobalShortcuts: bind one shortcut, print Activated/Deactivated
  screencast     ScreenCast: monitor stream, restore token, grab one frame as PNG
  screenshot     Screenshot portal fallback: one PNG
  remotedesktop  RemoteDesktop: pointer+keyboard session, move pointer, probe ConnectToEIS
  pwgrab         debug: grab one frame from a node on the default PipeWire daemon
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "info":
		err = runInfo(ctx)
	case "shortcuts":
		err = runShortcuts(ctx, args)
	case "screencast":
		err = runScreencast(ctx, args)
	case "screenshot":
		err = runScreenshot(ctx, args)
	case "remotedesktop":
		err = runRemoteDesktop(ctx, args)
	case "pwgrab":
		err = runPWGrab(ctx, args)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s portals %s: %v\n", ts(), cmd, err)
		os.Exit(1)
	}
}

func ts() string { return time.Now().Format("15:04:05.000") }

func logf(format string, a ...any) { fmt.Printf(ts()+" "+format+"\n", a...) }

func since(t time.Time) time.Duration { return time.Since(t).Round(time.Millisecond) }

func connect() (*portal.Conn, error) {
	c, err := portal.Connect()
	if err != nil {
		return nil, err
	}
	return c, nil
}

func runInfo(ctx context.Context) error {
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Printf("XDG_CURRENT_DESKTOP=%s\n", os.Getenv("XDG_CURRENT_DESKTOP"))
	for _, iface := range []string{portal.IfGlobalShortcuts, portal.IfScreenCast, portal.IfRemoteDesktop, portal.IfScreenshot} {
		fmt.Printf("%s version=%d\n", iface, c.Version(iface))
	}
	for _, p := range []struct{ iface, name string }{
		{portal.IfScreenCast, "AvailableSourceTypes"},
		{portal.IfScreenCast, "AvailableCursorModes"},
		{portal.IfRemoteDesktop, "AvailableDeviceTypes"},
	} {
		v, err := c.Property(p.iface, p.name)
		if err != nil {
			fmt.Printf("%s.%s error=%v\n", p.iface, p.name, err)
			continue
		}
		fmt.Printf("%s.%s=%v\n", p.iface, p.name, v.Value())
	}
	fmt.Printf("pipewire frame grab compiled in (cgo)=%v\n", pipewire.Available)
	bus, err := dbus.ConnectSessionBus()
	if err == nil {
		defer bus.Close()
		for _, n := range []string{
			"org.gnome.Mutter.ScreenCast", "org.gnome.Mutter.RemoteDesktop",
			"org.gnome.Shell.Screenshot", "org.gnome.Settings.GlobalShortcutsProvider",
			"org.freedesktop.impl.portal.desktop.gnome", "org.freedesktop.impl.portal.desktop.kde",
			"org.freedesktop.impl.portal.desktop.wlr", "org.freedesktop.impl.portal.desktop.hyprland",
		} {
			var has bool
			_ = bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, n).Store(&has)
			fmt.Printf("bus %s owned=%v\n", n, has)
		}
	}
	confs, _ := filepath.Glob("/usr/share/xdg-desktop-portal/*portals.conf")
	home, _ := os.UserConfigDir()
	userConfs, _ := filepath.Glob(filepath.Join(home, "xdg-desktop-portal", "*portals.conf"))
	for _, f := range append(confs, userConfs...) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		fmt.Printf("--- %s\n%s", f, b)
	}
	return nil
}

func runShortcuts(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("shortcuts", flag.ExitOnError)
	trigger := fs.String("trigger", "CTRL+ALT+space", "preferred_trigger (xdg shortcut syntax)")
	dur := fs.Duration("for", 60*time.Second, "how long to listen for Activated/Deactivated after binding")
	timeout := fs.Duration("timeout", 60*time.Second, "timeout for CreateSession/BindShortcuts (consent dialog)")
	fs.Parse(args)

	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	logf("GlobalShortcuts version=%d", c.Version(portal.IfGlobalShortcuts))
	rctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	t := time.Now()
	sess, err := c.CreateSession(rctx, portal.IfGlobalShortcuts, nil)
	if err != nil {
		return fmt.Errorf("CreateSession: %w", err)
	}
	defer c.CloseSession(sess)
	logf("CreateSession ok in %v", since(t))

	act, cancelA, err := c.Subscribe("/org/freedesktop/portal/desktop", portal.IfGlobalShortcuts, "Activated")
	if err != nil {
		return err
	}
	defer cancelA()
	deact, cancelD, err := c.Subscribe("/org/freedesktop/portal/desktop", portal.IfGlobalShortcuts, "Deactivated")
	if err != nil {
		return err
	}
	defer cancelD()

	type shortcut struct {
		ID   string
		Opts map[string]dbus.Variant
	}
	sc := []shortcut{{ID: "talk", Opts: map[string]dbus.Variant{
		"description":       dbus.MakeVariant("openclick spike: hold to talk"),
		"preferred_trigger": dbus.MakeVariant(*trigger),
	}}}
	t = time.Now()
	logf("BindShortcuts: a consent/assignment dialog may appear now")
	res, err := c.Request(rctx, portal.IfGlobalShortcuts+".BindShortcuts", nil, sess, sc, "")
	if err != nil {
		return fmt.Errorf("BindShortcuts after %v: %w", since(t), err)
	}
	logf("BindShortcuts ok in %v: %s", since(t), describeShortcuts(res["shortcuts"]))

	logf("press and hold the shortcut; listening %v for Activated/Deactivated", *dur)
	var last time.Time
	end := time.After(*dur)
	for {
		select {
		case s := <-act:
			last = time.Now()
			logf("Activated   id=%v portal-ts=%v", bodyAt(s, 1), bodyAt(s, 2))
		case s := <-deact:
			held := ""
			if !last.IsZero() {
				held = fmt.Sprintf(" held=%v", since(last))
			}
			logf("Deactivated id=%v portal-ts=%v%s", bodyAt(s, 1), bodyAt(s, 2), held)
		case <-end:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func describeShortcuts(v dbus.Variant) string {
	list, ok := v.Value().([][]any)
	if !ok {
		return fmt.Sprintf("%v", v.Value())
	}
	var parts []string
	for _, e := range list {
		if len(e) < 2 {
			continue
		}
		opts, _ := e[1].(map[string]dbus.Variant)
		parts = append(parts, fmt.Sprintf("%v trigger=%q", e[0], fmt.Sprint(opts["trigger_description"].Value())))
	}
	return strings.Join(parts, ", ")
}

func bodyAt(s *dbus.Signal, i int) any {
	if i < len(s.Body) {
		return s.Body[i]
	}
	return nil
}

func tokenPath(name string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "openclick-spike", name+".token")
}

func readToken(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeToken(path, tok string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

type stream struct {
	node  uint32
	props map[string]dbus.Variant
}

func parseStreams(v dbus.Variant) []stream {
	list, _ := v.Value().([][]any)
	var out []stream
	for _, e := range list {
		if len(e) < 2 {
			continue
		}
		n, _ := e[0].(uint32)
		p, _ := e[1].(map[string]dbus.Variant)
		out = append(out, stream{n, p})
	}
	return out
}

func runScreencast(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("screencast", flag.ExitOnError)
	timeout := fs.Duration("timeout", 90*time.Second, "overall timeout (includes the consent dialog)")
	tokFile := fs.String("token-file", tokenPath("screencast"), "restore token file (read if present, rewritten after Start)")
	noToken := fs.Bool("no-token", false, "ignore an existing restore token")
	dmabuf := fs.Bool("dmabuf", true, "offer LINEAR DMA-BUF formats to the producer")
	implicit := fs.Bool("implicit-modifier", false, "also offer DRM_FORMAT_MOD_INVALID (driver-chosen layout)")
	out := fs.String("out", "", "output directory for the PNG (default: new temp dir)")
	hold := fs.Duration("hold", 0, "keep the session open this long after the grab (inspect with pw-dump)")
	fs.Parse(args)

	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	ver := c.Version(portal.IfScreenCast)
	logf("ScreenCast version=%d", ver)
	rctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	total := time.Now()
	t := time.Now()
	sess, err := c.CreateSession(rctx, portal.IfScreenCast, nil)
	if err != nil {
		return fmt.Errorf("CreateSession: %w", err)
	}
	defer c.CloseSession(sess)
	logf("CreateSession ok in %v", since(t))

	opts := map[string]dbus.Variant{
		"types":    dbus.MakeVariant(uint32(1)), // MONITOR
		"multiple": dbus.MakeVariant(false),
	}
	if ver >= 2 {
		opts["cursor_mode"] = dbus.MakeVariant(uint32(1)) // hidden: the model must not see our overlay cursor
	}
	tok := ""
	if !*noToken {
		tok = readToken(*tokFile)
	}
	if ver >= 4 {
		opts["persist_mode"] = dbus.MakeVariant(uint32(2)) // until revoked
		if tok != "" {
			opts["restore_token"] = dbus.MakeVariant(tok)
		}
	}
	logf("restore token supplied=%v", tok != "")
	t = time.Now()
	if _, err := c.Request(rctx, portal.IfScreenCast+".SelectSources", opts, sess); err != nil {
		return fmt.Errorf("SelectSources: %w", err)
	}
	logf("SelectSources ok in %v", since(t))

	t = time.Now()
	logf("Start: the monitor picker / consent dialog appears now unless the token is accepted")
	res, err := c.Request(rctx, portal.IfScreenCast+".Start", nil, sess, "")
	if err != nil {
		return fmt.Errorf("Start after %v: %w", since(t), err)
	}
	logf("Start ok in %v", since(t))
	if nt, ok := res["restore_token"].Value().(string); ok && nt != "" {
		if err := writeToken(*tokFile, nt); err != nil {
			return err
		}
		// Never print the token itself; it grants capture without consent.
		logf("restore_token received (len %d), saved to %s", len(nt), *tokFile)
	} else {
		logf("no restore_token returned")
	}
	streams := parseStreams(res["streams"])
	if len(streams) == 0 {
		return errors.New("no streams")
	}
	for _, s := range streams {
		logf("stream node=%d position=%v size=%v source_type=%v", s.node,
			s.props["position"].Value(), s.props["size"].Value(), s.props["source_type"].Value())
	}

	t = time.Now()
	var fd dbus.UnixFD
	if err := c.Call(rctx, portal.IfScreenCast+".OpenPipeWireRemote", sess, map[string]dbus.Variant{}).Store(&fd); err != nil {
		return fmt.Errorf("OpenPipeWireRemote: %w", err)
	}
	defer closeFD(int(fd))
	logf("OpenPipeWireRemote ok in %v (fd %d)", since(t), fd)

	if *hold > 0 {
		defer func() {
			logf("holding session %v", *hold)
			time.Sleep(*hold)
		}()
	}
	t = time.Now()
	fr, err := pipewire.GrabFrame(rctx, int(fd), streams[0].node, pipewire.Options{AllowDMABuf: *dmabuf, ImplicitModifier: *implicit})
	if err != nil {
		return fmt.Errorf("first frame after %v: %w", since(t), err)
	}
	logf("first frame in %v: %dx%d format=%s data=%s modifier=%#x(has=%v)", since(t),
		fr.Image.Bounds().Dx(), fr.Image.Bounds().Dy(), fr.Format, fr.DataType, fr.Modifier, fr.HasModifier)
	path, err := savePNG(*out, "screencast.png", fr.Image)
	if err != nil {
		return err
	}
	logf("saved %s; total CreateSession->PNG %v", path, since(total))
	return nil
}

func outDir(dir string) (string, error) {
	if dir != "" {
		return dir, os.MkdirAll(dir, 0o700)
	}
	return os.MkdirTemp("", "openclick-oc6-")
}

func savePNG(dir, name string, img image.Image) (string, error) {
	dir, err := outDir(dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return path, png.Encode(f, img)
}

func closeFD(fd int) { _ = syscall.Close(fd) }

func runScreenshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("screenshot", flag.ExitOnError)
	timeout := fs.Duration("timeout", 60*time.Second, "timeout (includes a permission dialog)")
	interactive := fs.Bool("interactive", false, "let the user pick the area in the backend UI")
	out := fs.String("out", "", "output directory (default: new temp dir)")
	fs.Parse(args)

	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	logf("Screenshot version=%d", c.Version(portal.IfScreenshot))
	rctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	t := time.Now()
	res, err := c.Request(rctx, portal.IfScreenshot+".Screenshot",
		map[string]dbus.Variant{"interactive": dbus.MakeVariant(*interactive)}, "")
	if err != nil {
		return fmt.Errorf("Screenshot after %v: %w", since(t), err)
	}
	took := since(t)
	uri, _ := res["uri"].Value().(string)
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return fmt.Errorf("unexpected uri scheme in result")
	}
	src, err := os.Open(u.Path)
	if err != nil {
		return err
	}
	defer src.Close()
	img, err := png.Decode(src)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	dir, err := outDir(*out)
	if err != nil {
		return err
	}
	dst := filepath.Join(dir, "screenshot.png")
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, src); err != nil {
		return err
	}
	// The portal leaves its copy in ~/Pictures or similar; remove it so
	// screen content does not linger on disk (FR-SCREEN-006).
	_ = os.Remove(u.Path)
	logf("Screenshot ok in %v: %dx%d saved %s (portal file removed)", took, img.Bounds().Dx(), img.Bounds().Dy(), dst)
	return nil
}

func runRemoteDesktop(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("remotedesktop", flag.ExitOnError)
	timeout := fs.Duration("timeout", 90*time.Second, "overall timeout (includes the consent dialog)")
	tokFile := fs.String("token-file", tokenPath("remotedesktop"), "restore token file")
	noToken := fs.Bool("no-token", false, "ignore an existing restore token")
	fs.Parse(args)

	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	ver := c.Version(portal.IfRemoteDesktop)
	logf("RemoteDesktop version=%d (ConnectToEIS needs >=2)", ver)
	if v, err := c.Property(portal.IfRemoteDesktop, "AvailableDeviceTypes"); err == nil {
		logf("AvailableDeviceTypes=%v (1=keyboard 2=pointer 4=touch)", v.Value())
	}
	rctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	t := time.Now()
	sess, err := c.CreateSession(rctx, portal.IfRemoteDesktop, nil)
	if err != nil {
		return fmt.Errorf("CreateSession: %w", err)
	}
	defer c.CloseSession(sess)
	logf("CreateSession ok in %v", since(t))

	opts := map[string]dbus.Variant{"types": dbus.MakeVariant(uint32(1 | 2))}
	tok := ""
	if !*noToken {
		tok = readToken(*tokFile)
	}
	if ver >= 2 {
		opts["persist_mode"] = dbus.MakeVariant(uint32(2))
		if tok != "" {
			opts["restore_token"] = dbus.MakeVariant(tok)
		}
	}
	logf("restore token supplied=%v", tok != "")
	t = time.Now()
	if _, err := c.Request(rctx, portal.IfRemoteDesktop+".SelectDevices", opts, sess); err != nil {
		return fmt.Errorf("SelectDevices: %w", err)
	}
	logf("SelectDevices ok in %v", since(t))

	t = time.Now()
	logf("Start: the remote-control consent dialog appears now unless the token is accepted")
	res, err := c.Request(rctx, portal.IfRemoteDesktop+".Start", nil, sess, "")
	if err != nil {
		return fmt.Errorf("Start after %v: %w", since(t), err)
	}
	logf("Start ok in %v devices=%v", since(t), res["devices"].Value())
	if nt, ok := res["restore_token"].Value().(string); ok && nt != "" {
		if err := writeToken(*tokFile, nt); err != nil {
			return err
		}
		logf("restore_token received (len %d), saved to %s", len(nt), *tokFile)
	}

	t = time.Now()
	for i := 0; i < 5; i++ {
		if err := c.Call(rctx, portal.IfRemoteDesktop+".NotifyPointerMotion", sess, map[string]dbus.Variant{}, 4.0, 0.0).Err; err != nil {
			return fmt.Errorf("NotifyPointerMotion: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	logf("NotifyPointerMotion x5 (+20px total) ok in %v; the cursor should have moved right", since(t))

	if ver >= 2 {
		var fd dbus.UnixFD
		err := c.Call(rctx, portal.IfRemoteDesktop+".ConnectToEIS", sess, map[string]dbus.Variant{}).Store(&fd)
		if err != nil {
			logf("ConnectToEIS unavailable: %v", err)
		} else {
			logf("ConnectToEIS ok: got libei socket fd %d (libei client not implemented in spike)", fd)
			closeFD(int(fd))
		}
	}
	return nil
}

func runPWGrab(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pwgrab", flag.ExitOnError)
	node := fs.Uint("node", 0, "PipeWire node id on the default daemon")
	dmabuf := fs.Bool("dmabuf", false, "offer LINEAR DMA-BUF formats")
	timeout := fs.Duration("timeout", 10*time.Second, "timeout")
	out := fs.String("out", "", "output directory (default: new temp dir)")
	fs.Parse(args)
	if *node == 0 {
		return errors.New("-node required")
	}
	rctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	t := time.Now()
	fr, err := pipewire.GrabFrame(rctx, -1, uint32(*node), pipewire.Options{AllowDMABuf: *dmabuf})
	if err != nil {
		return err
	}
	logf("first frame in %v: %dx%d format=%s data=%s", since(t), fr.Image.Bounds().Dx(), fr.Image.Bounds().Dy(), fr.Format, fr.DataType)
	path, err := savePNG(*out, "pwgrab.png", fr.Image)
	if err != nil {
		return err
	}
	logf("saved %s", path)
	return nil
}
