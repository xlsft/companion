# OC-5 spike: Linux overlay

- Date: 2026-10-24
- Ticket: OC-5 (validates the "Overlay" row of ADR-0001 for Linux)
- Code: `internal/platform/linuxwayland/wl` (wire client), `internal/platform/linuxwayland/overlay`, `internal/platform/linuxx11/overlay`, `internal/platform/pixconv`, `cmd/spikes/overlay`
- Test machine: Arch Linux, niri 26.04 (Smithay), one output DP-2 2560x1080@200 Hz scale 1, xwayland-satellite 0.8.3 on `DISPLAY=:0`

## Verdict

| Session | Result |
|---|---|
| Wayland + wlr-layer-shell (niri) | **Works.** Transparent, above everything, click-through, no focus change, interactive sub-region works. |
| X11 backend under xwayland-satellite (niri) | **Does not work** there: the window becomes a tiled, focused, opaque toplevel. Not a target: on a Wayland session we always use layer-shell. |
| Native X11 session | Compiles; not run (no X11 session on the test machine). Manual checklist below. |
| GNOME (Mutter, no layer-shell) | Expected `ErrUnsupported` from `overlay.New` → fallback panel. |

## Design choices

**Pure-Go Wayland client, no cgo.** The overlay needs about a dozen requests on nine interfaces (`wl_display`, `wl_registry`, `wl_compositor`, `wl_shm(_pool)`, `wl_buffer`, `wl_surface`, `wl_region`, `wl_output`, `wl_seat/wl_pointer`, `zwlr_layer_shell_v1`, `wp_fractional_scale_v1`, `wp_viewporter`, plus `zwlr_virtual_pointer_v1` for the self-test). The wire format is simple (32-bit LE words, length-prefixed strings, fds over `SCM_RIGHTS`), so `wl` is ~350 lines with no code generator and no third-party module. Compared with:

- `rajveermalviya/go-wayland`: generated bindings with sparse maintenance; wlr-layer-shell and wlr-virtual-pointer are not part of its core set, so we would generate and vendor them anyway, and we would still own the threading model.
- cgo + libwayland-client: costs a C toolchain on every Linux build and `linux && cgo` tags, for no functional gain here.

The X11 backend uses `github.com/jezek/xgb` (pure Go, has SHAPE and RandR 1.5), so it also needs no libX11/Xext/Xfixes at build time. Everything is `//go:build linux` only; `make cross` stays green.

**Drawing.** Frames are `*image.RGBA` (premultiplied) rendered by shared Go code at native resolution; `pixconv.RGBAToBGRA` swaps to ARGB8888 for `wl_shm` and X11 depth-32 ZPixmap. Gio is not involved (ADR-0001).

## Wayland implementation (`linuxwayland/overlay`)

Per `wl_output`, one surface:

- `zwlr_layer_shell_v1.get_layer_surface(surface, output, layer=overlay(3), namespace="openclick-overlay")`
- `set_anchor(top|bottom|left|right)`, `set_size(0,0)`, `set_exclusive_zone(-1)` (ignore panels' exclusive zones, cover the full output), `set_keyboard_interactivity(none)`
- input region = empty `wl_region` → click-through; `SetInteractive(rect)` replaces it with a one-rect region and commits; empty `Rect` restores the empty region
- no opaque region
- buffers: `memfd` + `wl_shm_pool`, ARGB8888, up to 3 in flight, recycled on `wl_buffer.release`
- scale: when `wp_fractional_scale_manager_v1` + `wp_viewporter` exist, buffer is native pixels and `wp_viewport.set_destination(logical)`; otherwise `wl_surface.set_buffer_scale(round(native/logical))` from `wl_output.scale`
- `DisplayID` = `wl:<wl_output.name>` (e.g. `wl:DP-2`), stable across reconnects (wl_output v4)
- `New` returns `contracts.CapabilityError{CapOverlayClickThrough, ErrUnsupported}` when the compositor has no `zwlr_layer_shell_v1`
- `layer_surface.closed` (output unplugged) marks the surface closed; `Present` then fails and the caller reopens on the new topology

niri advertises: `zwlr_layer_shell_v1 v5`, `wp_fractional_scale_manager_v1`, `wp_viewporter`, `wp_presentation v2`, `zwlr_virtual_pointer_manager_v1 v2`, `zwlr_screencopy_manager_v1 v3`, `ext_foreign_toplevel_list_v1` and more (run `overlay -list`).

## Measurements on niri

```
$ go run ./cmd/spikes/overlay -seconds 3 -selftest
backend=wayland focused-before="Window ID 4: (focused)"
fractional-scale+viewporter=true
display=wl:DP-2 native=2560x1080 scale=1.000 open=140µs render=1.457ms present-call=7.772ms
display=wl:DP-2 first-frame-callback=6.485ms
bench frames=30 present-call p50=2.625ms p95=2.908ms | commit->frame-callback p50=4ms p95=5.357ms max=5.661ms
focused-after-map="Window ID 4: (focused)" stolen=false
selftest pass-through=true interactive-region=true restored=true
focused-after-selftest="Window ID 4: (focused)" stolen=false
```

- **Present time**: full-screen 2560x1080 frame, `present-call` (RGBA→BGRA copy into shm + attach/damage/commit) p50 2.6 ms; commit → `wl_callback.done` p50 4.0 ms, p95 5.4 ms at 200 Hz. The first present on a fresh buffer is ~8 ms (memfd + mmap + page faults). Rendering the test frame in Go: ~1.4 ms. Full-screen re-uploads every frame are affordable; damage tracking (OC-overlay renderer) will cut it further.
- **Focus**: `niri msg focused-window` unchanged before map, after map, after the self-test clicks and at exit; `niri msg event-stream` logged no focus-change events during a full run. `niri msg layers` shows the surface as `Namespace: "openclick-overlay" / Keyboard interactivity: none`. (In two runs the focused window changed between the start and end because the user switched windows; the event stream shows it is unrelated.)
- **Click-through, verified automatically** with `-selftest` (no ydotool/wtype on the machine; niri offers `zwlr_virtual_pointer_v1`): the spike maps an opaque 1000x400 "victim" layer surface on the `top` layer (under `overlay`) at the output centre, moves a virtual pointer there and clicks.
  1. overlay fully click-through → victim gets `enter` + `button 0x110 press/release`, overlay gets nothing ✔
  2. `SetInteractive(label card)` → pointer moved into the card → overlay gets `enter` + button, victim gets `leave` and no button ✔
  3. `SetInteractive(Rect{})` → victim receives enter + button again ✔
  Clicks are only sent after the pointer is confirmed over one of the spike's own surfaces, so a failing test never clicks user windows. The cursor does move.
- Visual check: `grim` screenshot during the run shows the ring, arrow and label composited over the windows below with correct alpha (premultiplied ARGB8888 correct).

## X11 implementation (`linuxx11/overlay`)

Per RandR 1.5 monitor (fallback: whole root), one window:

- depth-32 TrueColor visual + own colormap, `CWBackPixel=0`, `CWBorderPixel=0`, `CWOverrideRedirect=1` (WM never manages/focuses it)
- `SHAPE` input region set to zero rectangles → click-through; `SetInteractive(rect)` sets one rectangle
- `_NET_WM_STATE=[_NET_WM_STATE_ABOVE]`, `_NET_WM_WINDOW_TYPE=[_NET_WM_WINDOW_TYPE_NOTIFICATION]`, `_NET_WM_BYPASS_COMPOSITOR=2` — ignored by WMs for override-redirect windows but read by compositors
- frames via `PutImage` ZPixmap in strips < 256 KiB (no BIG-REQUESTS dependency); "latency" = PutImage + `GetInputFocus` roundtrip (~5.5 ms full screen on Xwayland). MIT-SHM would halve that if needed.
- `Composited()` checks the `_NET_WM_CM_S0` owner; without a compositor ARGB renders black → report `overlay_click_through` degraded and use the fallback panel.
- Override-redirect windows are stacked above normal windows when mapped but not kept above windows raised later; production code must re-raise (`ConfigureWindow StackMode=Above`) on `VisibilityNotify`/periodically. Fullscreen games with unredirection can hide it.
- Scale: X11 has no per-monitor scale; `Scale()=1`, HiDPI is via global `Xft.dpi` (OC-coordinates must take it from there).

### Under xwayland-satellite (niri)

```
$ go run ./cmd/spikes/overlay -backend x11 -seconds 3
backend=x11 focused-before="Window ID 4: (focused)"
x11 composited=true
display=x11:DP-2 native=2560x1080 ...
focused-after-map="Window ID 16: (focused)" stolen=true
```

xwayland-satellite (rootless Xwayland as an ordinary Wayland client) has no notion of override-redirect overlays: it maps the window as a tiled toplevel (`niri msg focused-window` → Title/App ID unset, PID of satellite), it **takes focus**, and the alpha channel is dropped (opaque black frame). So: never use the X11 backend on a Wayland session. On GNOME/KDE the Xwayland server is integrated in the compositor and override-redirect + ARGB do work there, but they would still not be above Wayland-native fullscreen windows; layer-shell (KDE) or fallback panel (GNOME) are the right choice.

### Manual checklist for a native X11 session (i3/Openbox/Xfce + picom, or GNOME/KDE on Xorg)

1. `go run ./cmd/spikes/overlay -backend x11 -seconds 15`
2. `composited=true` printed; ring/arrow/label visible with transparent background (no black rectangle). Without a compositor (`pkill picom`) expect black → confirms the `Composited()` probe.
3. `focused-after-map` equals `focused-before` (X input focus id unchanged); type in a terminal during the run: keystrokes keep going to it.
4. Phase 1/3: click a window/link visible through the overlay → it reacts. Phase 2 (middle third): clicking the label card does nothing below (it is the interactive region).
5. Raise another window during the run: note whether the overlay stays on top (expected: may be covered; needs re-raise).

## Expected behaviour on other compositors

| Compositor | Layer-shell | Expectation |
|---|---|---|
| niri | yes (v5) | Verified above. |
| Sway / river / labwc / other wlroots | yes | Same code path; wlroots implements empty input region and `keyboard_interactivity=none` the same way. Fractional scale via viewporter supported (Sway ≥ 1.9). |
| Hyprland | yes | Same path. Hyprland may apply layer rules (blur, animations) by namespace; `openclick-overlay` should be documented for users who want `layerrule = noanim`. |
| KDE Plasma 6 (KWin) | yes (`zwlr_layer_shell_v1`, Plasma 5.20+) | Same path expected; KWin honours empty input regions. Verify on a Plasma VM (OC-5 follow-up); KWin draws layer `overlay` above fullscreen windows. |
| GNOME (Mutter) | **no** | `overlay.New` → `ErrUnsupported` (`compositor has no zwlr_layer_shell_v1`). Use the fallback panel (annotated capture in a normal Gio window, FR-OVERLAY-009). An xdg_toplevel cannot be made always-on-top or click-through on Mutter; a GNOME Shell extension would be the only path to a real overlay and is out of scope. |
| Weston / Cosmic | Cosmic: yes; Weston: no | Cosmic same path; Weston → fallback. |

## Capability reporting

- Wayland with layer-shell: `overlay_click_through = available`, backend `wlr-layer-shell`.
- Wayland without layer-shell: `overlay_click_through = unavailable`, fallback `overlay_fallback_panel`, reason `no zwlr_layer_shell_v1`.
- X11 with compositor: `available`, backend `x11-shape-override-redirect`; without compositor: `unavailable` → fallback panel.
- Wayland session with only Xwayland: never pick X11.

## Open issues

- Multi-output and fractional scale (>1) not exercised (single 1x output here); code paths exist (`wp_viewport` destination, `preferred_scale`). Needs a run on a HiDPI/multi-monitor setup.
- `Display.Bounds` comes from `wl_output.geometry`; use `zxdg_output_manager_v1` (logical position/size) in the real `DisplayService`.
- Frame timing uses `wl_surface.frame`; `wp_presentation` (available on niri) would give true scan-out time for OC-overlay latency budgets.
- Output hotplug: surfaces get `closed` but the host doesn't yet re-open on new outputs; that belongs in the overlay manager with `DisplayService.Watch`.
- X11: re-raise policy, MIT-SHM upload, verify on a real X session.
- The X11 backend silences xgb's global logger.
