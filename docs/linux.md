# Linux

Companion on Linux is two binaries that must sit in the same directory: `companion` (Gio UI, core) and `stt-sidecar` (speech recognition: sherpa-onnx, with whisper.cpp as the fallback engine). The core looks for the sidecar next to its own executable (symlinks are resolved). The sidecar loads `libsherpa-onnx-c-api.so` and `libonnxruntime.so` (prebuilt, CPU, from the sherpa-onnx Go module) from its own directory through an `$ORIGIN` runpath, so they ship beside it in the tarball, the AppImage (`usr/bin`) and the Arch package (`/usr/lib/companion`). Speech models are never bundled; they are downloaded on first run into `~/.config/openclick/models` (or the `-data` directory).

Build and packaging commands are at the end of this page.

## Supported systems

| Platform | Status |
|---|---|
| Linux, Wayland (GNOME, KDE Plasma, sway, Hyprland, niri, other wlroots compositors) | Supported; capabilities vary by compositor and portal backend, see [Capability profiles](#capability-profiles) |
| Linux, X11 | Supported |
| Windows, macOS | Not yet: platform backends are not implemented, no release builds |

Release binaries are x86_64 and aarch64, built on Ubuntu 24.04, and need glibc 2.39 or newer (Ubuntu 24.04, Debian 13,
Fedora 40 and later, Arch); on older systems [build from source](development.md#building-from-source).

## Install

### Install script

```sh
curl -fsSL https://raw.githubusercontent.com/xlsft/companion/main/install.sh | sh
```

It downloads the latest release, checks its SHA-256 and, if [cosign](https://docs.sigstore.dev/cosign/system_config/installation/)
is installed, its signature (see [SECURITY.md](../SECURITY.md#verifying-releases)), then installs into `~/.local`.

| Variable | |
|---|---|
| `VERSION=0.1.0` | install that release instead of the latest |
| `PREFIX=/opt/companion` | install somewhere else than `~/.local` |
| `REQUIRE_SIGNATURE=1` | refuse to install unless cosign verifies the signature |
| `GITHUB_TOKEN=…` | use a token for the GitHub API (rate limits) |

Run the same command again to upgrade. To remove it (settings and models in `~/.config/openclick` stay):

```sh
curl -fsSL https://raw.githubusercontent.com/xlsft/companion/main/install.sh | sh -s -- --uninstall
```

### Homebrew

`brew install xlsft/tap/companion` (Homebrew on Linux).

### First run

Run `companion` (or pick Companion in the app menu); the setup wizard opens:

1. **Permissions**: microphone, screen capture, hotkey and overlay are each checked for real. Where the desktop does
   not let apps grab a key combination, the wizard shows the binding to paste into your compositor or desktop settings
   (`companion ctl talk-toggle`); see [Binding the hotkey without a portal](#binding-the-hotkey-without-a-portal).
2. **Model**: enter a base URL, model and API key. The wizard checks text, streaming, vision and tool use.
3. **Speech recognition**: download the recommended model and say the test phrase.
4. **Voice**: pick a speech provider and voice, or leave answers as text.
5. Press **Super+Space** and ask about what is on the screen.

### Tarball

```sh
tar xzf companion-<version>-linux-x86_64.tar.gz
cd companion-<version>-linux-x86_64
./companion                 # run in place
./install.sh                # or install under ~/.local (PREFIX=/usr as root for system-wide)
```

`install.sh` puts both binaries and the two sherpa-onnx libraries into `$PREFIX/lib/companion`, symlinks `companion` into `$PREFIX/bin` and installs the desktop entry, icon and AppStream metainfo.

### AppImage

```sh
chmod +x Companion-<version>-x86_64.AppImage
./Companion-<version>-x86_64.AppImage
```

The AppImage is deliberately thin: it does not bundle the graphics stack, because EGL, Wayland and Mesa must match the host. It needs the same runtime libraries as the tarball (below) and FUSE 2 (`libfuse2`), or run it with `--appimage-extract-and-run`. For compositor key bindings use the AppImage path as the command (`/path/to/Companion.AppImage ctl talk-toggle`); each key press then starts a short-lived AppImage mount, so for bindings the tarball install is faster and preferable.

### Arch Linux

`packaging/PKGBUILD` builds from a source checkout (see the comments in the file); it installs to `/usr/lib/companion` with `/usr/bin/companion` as a symlink.

## Runtime dependencies

| Need | Why | Arch | Fedora | Ubuntu / Debian |
|---|---|---|---|---|
| libEGL, Wayland client libs | Gio window (Wayland) | `libglvnd wayland` | `libglvnd-egl libwayland-client libwayland-egl libwayland-cursor` | `libegl1 libwayland-client0 libwayland-egl1 libwayland-cursor0` |
| X11 + xkbcommon | Gio window (X11, XWayland), X11 backends | `libx11 libxcursor libxfixes libxkbcommon libxkbcommon-x11 libxcb` | `libX11 libXcursor libXfixes libxkbcommon libxkbcommon-x11 libxcb` | `libx11-6 libxcursor1 libxfixes3 libxkbcommon0 libxkbcommon-x11-0 libx11-xcb1 libxcb1` |
| Mesa/GL driver | rendering | `mesa` | `mesa-dri-drivers` | `libgl1-mesa-dri` |
| libstdc++, libgomp | `stt-sidecar` (whisper.cpp, sherpa-onnx, onnxruntime) | `gcc-libs` | `libstdc++ libgomp` | `libstdc++6 libgomp1` |
| PipeWire (with pipewire-pulse) or PulseAudio | microphone and speaker via miniaudio | `pipewire pipewire-pulse` | `pipewire pipewire-pulseaudio` | `pipewire pipewire-pulse` |
| xdg-desktop-portal and a backend | screenshot, global shortcuts, remote desktop (below) | `xdg-desktop-portal` + backend | `xdg-desktop-portal` + backend | `xdg-desktop-portal` + backend |
| Secret Service provider | API keys (never written to disk) | `gnome-keyring` or `kwallet` or `keepassxc` | same | same |
| AT-SPI (optional) | accessibility tree for screen context | `at-spi2-core` | `at-spi2-core` | `at-spi2-core` |

Miniaudio loads PulseAudio/ALSA/JACK at runtime (no link-time dependency), so the package names for audio servers are about what must be running, not what the binary links. `ldd companion` should show no "not found" lines.

The system tray icon needs a StatusNotifierWatcher (KDE has one; GNOME needs the AppIndicator extension; wlroots compositors need a bar with a tray such as waybar). Without one the tray is skipped and the app stays window-only.

### Portals

Companion talks to `org.freedesktop.portal.Desktop` for these interfaces; install the backend that matches your desktop:

| Interface | Used for | Backends that provide it |
|---|---|---|
| `GlobalShortcuts` | the Talk hotkey on Wayland | xdg-desktop-portal-kde; xdg-desktop-portal-gnome (recent versions, uncertain which); xdg-desktop-portal-hyprland |
| `Screenshot` | screen capture when `wlr-screencopy` is missing | all full backends (-gnome, -kde, -gtk, -wlr, -hyprland) |
| `RemoteDesktop` | click/type/text insertion on Wayland (consent on first use) | -gnome, -kde, -hyprland (uncertain: -wlr has none) |
| `ScreenCast` | detected, but not yet a capture backend | -gnome, -kde, -wlr, -hyprland |

Backend packages: `xdg-desktop-portal-gnome`, `xdg-desktop-portal-kde`, `xdg-desktop-portal-wlr`, `xdg-desktop-portal-hyprland`, `xdg-desktop-portal-gtk` (same names on Arch, Fedora and Debian/Ubuntu). Run `companion -diagnose` to print the capability report, including `portal.<name>.version` facts (`absent` means that interface is not on the bus).

## Capability profiles

The runtime decides from facts, not from the desktop's name: `internal/platform/detect` maps session type, portal presence and Wayland globals to a per-capability status (`available`, `degraded`, `unavailable`, `unknown`). The table is what that code produces for each environment as far as the code shows; entries marked (?) depend on compositor or portal-backend versions that were not verified, and the authoritative answer on any machine is `companion -diagnose`.

| Capability | GNOME (Wayland) | KDE Plasma (Wayland) | sway | Hyprland | niri | X11 (any) |
|---|---|---|---|---|---|---|
| Global hotkey | GlobalShortcuts portal if the interface exists (?), else `companion ctl` binding | portal (?) | `companion ctl` binding | portal if `xdg-desktop-portal-hyprland` is up, else ctl | `companion ctl` binding always (the portal's GNOME backend refuses BindShortcuts on niri) | XGrabKey |
| Hold-to-talk (key release) | unknown: depends on portal backend delivering `Deactivated` | unknown (same) | binding with `bindsym --release`; unknown until one fires | binding with `bindr` | no, niri binds fire on press only; use toggle | yes |
| Screen capture | Screenshot portal (whole desktop, consent may be asked once) | Screenshot portal | `wlr-screencopy`, no consent | `wlr-screencopy` | Screenshot portal if present, else unavailable (?) | XGetImage |
| Overlay (click-through) | no layer-shell (?): falls back to an ordinary panel window | no `zwlr_layer_shell_v1` on KWin as probed (?): fallback panel | wlr-layer-shell | wlr-layer-shell | layer-shell if advertised (?), else fallback panel | X11 shape |
| Input injection, text insertion | RemoteDesktop portal, consent on first use | RemoteDesktop portal | portal only if a backend has RemoteDesktop (?), else unavailable | RemoteDesktop portal (?) | unavailable unless a RemoteDesktop portal exists | XTest |
| Microphone | PipeWire socket; else unknown (ALSA/Pulse not probed) | same | same | same | same | same |
| Secret store | org.freedesktop.secrets (gnome-keyring) | kwallet (Secret Service API) | needs a provider | needs a provider | needs a provider | needs a provider |
| Accessibility | `org.a11y.Bus` if running | same | same, usually not started | same | same | same |
| Tray | needs AppIndicator extension | native | needs a bar with a tray | needs a bar with a tray | needs a bar with a tray | depends on the panel |

The fallback panel (an ordinary Gio window) works wherever a display session exists, so the UI itself is never the missing piece.

If no display session is detected (SSH, tty), every display capability is reported unavailable and the UI cannot start.

### Binding the hotkey without a portal

Wherever `GlobalShortcuts` is absent or refused (sway, niri, most bare compositors), the core listens on `$XDG_RUNTIME_DIR/openclick/control.sock` and the compositor runs `companion ctl <command>` per key press. Commands: `talk-toggle`, `talk-press`, `talk-release`, `cancel`, `ping`. `ctl` is a quick client; it does not start the UI and exits non-zero if the core is not running. The setup wizard shows the same snippets with your actual binary path filled in.

niri (`~/.config/niri/config.kdl`; press-only, so toggle):

```kdl
binds {
    Mod+Space repeat=false { spawn "companion" "ctl" "talk-toggle"; }
    Mod+Escape repeat=false { spawn "companion" "ctl" "cancel"; }
}
```

sway (`~/.config/sway/config`):

```
bindsym $mod+space exec companion ctl talk-press
bindsym --release $mod+space exec companion ctl talk-release
bindsym $mod+Escape exec companion ctl cancel
```

Hyprland (`~/.config/hypr/hyprland.conf`):

```
bind  = SUPER, SPACE,  exec, companion ctl talk-press
bindr = SUPER, SPACE,  exec, companion ctl talk-release
bind  = SUPER, ESCAPE, exec, companion ctl cancel
```

GNOME, KDE and other desktops: add a custom keyboard shortcut in Settings with the command `companion ctl talk-toggle` (and one for `companion ctl cancel`). Use the full path if `companion` is not on `PATH` (for the tarball without `install.sh`, the path of the extracted binary). GNOME and KDE custom shortcuts fire on press, so use toggle.

## Building the packages

```sh
make whisper            # once: whisper.cpp static libraries (needs cmake)
make dist-linux         # dist/companion-<VERSION>-linux-<arch>.tar.gz and an AppImage
make dist-linux APPIMAGE=skip VERSION=0.1.0   # tarball only
```

`make dist-linux` builds the UI binary and the release sidecar (`make build-stt`: `-tags "whisper sherpa"`, `-engine auto`) itself, copies the sherpa-onnx libraries next to it, and refuses to pack a sidecar that lacks either engine or the libraries. With `patchelf` installed the sidecar's runpath is reduced to `$ORIGIN` (otherwise the build host's Go module cache stays as a harmless second entry). The AppImage step needs `mksquashfs` (`squashfs-tools`) and `appimagetool`; if `appimagetool` is not on `PATH`, version 1.9.1 is downloaded into `dist/.tools` and used only after its SHA-256 matches the pinned value in `packaging/dist-linux.sh`. If either is missing the target fails with a message (and the tarball has already been written). For a redistributable binary build whisper.cpp with `make whisper WHISPER_NATIVE=OFF` so ggml is not tuned to the build host's CPU.

The icon (`packaging/companion.svg`) is the Unbounded "↖" glyph in `#8b5cf6`, rendered to paths by `go run packaging/genicon.go > packaging/companion.svg`; the 256 px PNG is `rsvg-convert -w 256 -h 256` of that file. The window class and Wayland app id are both `companion` (Gio defaults to the executable name), which is why the desktop file is `companion.desktop` with `StartupWMClass=companion`: renaming the binary requires changing both.
