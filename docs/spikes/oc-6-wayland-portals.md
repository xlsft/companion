# OC-6 spike: Wayland portals (GlobalShortcuts, ScreenCast/PipeWire, Screenshot, RemoteDesktop)

- Date: 2026-10-24
- Ticket: OC-6 (validates the Wayland "Hotkeys", "Capture" and "Input" cells of ADR-0001)
- Code: `internal/platform/linuxwayland/portal` (godbus Request/Session client), `internal/platform/linuxwayland/pipewire` (one-frame grab, cgo behind `linux && cgo`), `cmd/spikes/portals`
- Test machine: Arch Linux, niri 26.04, NVIDIA RTX 4060 (proprietary driver), xdg-desktop-portal 1.22.1, xdg-desktop-portal-gnome 51.0, xdg-desktop-portal-gtk 1.15.3, PipeWire 1.6.10, libei 1.6.0

## Backend routing on niri

`XDG_CURRENT_DESKTOP=niri` → `/usr/share/xdg-desktop-portal/niri-portals.conf`:

```
[preferred]
default=gnome;gtk;
org.freedesktop.impl.portal.Access=gtk;
org.freedesktop.impl.portal.Notification=gtk;
org.freedesktop.impl.portal.Secret=gnome-keyring;
```

So ScreenCast, Screenshot, RemoteDesktop and GlobalShortcuts all go to **xdg-desktop-portal-gnome**, which in turn talks to the compositor through GNOME's private D-Bus APIs. niri implements some of them on the session bus (`portals info`):

| Private API xdg-desktop-portal-gnome needs | Owned on niri |
|---|---|
| `org.gnome.Mutter.ScreenCast` | yes |
| `org.gnome.Shell.Screenshot` | yes |
| `org.gnome.Mutter.RemoteDesktop` | **no** |
| GNOME Shell global-shortcuts provider | **no** |

## Results

| Portal | Version | niri result | Verdict |
|---|---|---|---|
| GlobalShortcuts | 2 | `CreateSession` ok (1 ms); `BindShortcuts` → Response 2 (failed) after 1 ms, no dialog | **Blocked on niri** |
| ScreenCast | 5 | Dialog "Share Screen" (xdg-desktop-portal-gnome); after approval `Start` 1.96 s; restore token works, `Start` 4 ms with token; `OpenPipeWireRemote` 1 ms; **frame grab fails** (see below) | **Works with limits** (consent + token ok, CPU frame read not on NVIDIA) |
| Screenshot | 2 | Non-interactive, no dialog (permission already "yes" for host apps), first call 2.7 s, then 220–280 ms, 2560x1080 PNG | **Works** — fallback for capture |
| RemoteDesktop | 2 | `AvailableDeviceTypes=0`, `CreateSession` → Response 2 | **Blocked on niri** |
| ConnectToEIS (libei) | needs RemoteDesktop ≥ 2 | interface version is 2, but no session can be created, so not reachable | **Blocked on niri** |

### GlobalShortcuts

```
$ portals shortcuts -timeout 15s -for 5s
GlobalShortcuts version=2
CreateSession ok in 1ms
BindShortcuts: a consent/assignment dialog may appear now
portals shortcuts: BindShortcuts after 1ms: portal: request failed
```

xdg-desktop-portal-gnome implements GlobalShortcuts by delegating to GNOME Shell, which niri does not provide. **Release events could not be measured here.** By the spec, `Activated`/`Deactivated` exist, so hold-to-talk is possible where the backend sends `Deactivated` on key-up:

- **KDE (xdg-desktop-portal-kde, KGlobalAccel)**: sends both `Activated` and `Deactivated` → `SupportsRelease()=true` expected. Shortcut chosen by the user in a KDE dialog the first time; persisted per app id.
- **GNOME 48+ (xdg-desktop-portal-gnome + Shell)**: GlobalShortcuts exists; a dialog asks the user to confirm/assign. Whether `Deactivated` is sent on key-up must be verified on GNOME (follow-up). Until then the session layer should start in toggle mode and switch to hold-to-talk only after a `Deactivated` was seen.
- **Hyprland (xdg-desktop-portal-hyprland)**: GlobalShortcuts with press and release; the binding is done in hyprland.conf (`bind = , key, global, app:id`), not by a dialog.
- **niri, Sway, other wlroots**: no GlobalShortcuts backend (xdg-desktop-portal-wlr has none). Fallback: user binds a compositor key to a CLI/IPC call (`companion ctl talk --toggle`, or press/release via `bind`/`bindr` on Sway, niri's `spawn` only on press) → toggle mode.

Capability mapping: `global_hotkey = unavailable` on niri with fallback "compositor keybinding → IPC"; `hotkey_release = unknown` until the first `Deactivated` arrives.

### ScreenCast + PipeWire

```
$ portals screencast -no-token -implicit-modifier          # first run
CreateSession ok in 1ms
SelectSources ok in 1ms            (types=MONITOR, cursor_mode=hidden, persist_mode=2)
Start: ...                         -> "Share Screen" window from xdg-desktop-portal-gnome
Start ok in 1.959s                 (time includes the user clicking Share)
restore_token received (len 22), saved to ~/.cache/openclick-spike/screencast.token
stream node=78 position=[0 0] size=[2560 1080] source_type=1
OpenPipeWireRemote ok in 1ms
first frame after 13s: pipewire: no frame (stream=streaming formats=1 buffers=1624 empty=0 corrupted=1624 type=3 format=8 modifier=0xffffffffffffff)

$ portals screencast -implicit-modifier                     # with restore token
restore token supplied=true
SelectSources ok in 5ms
Start ok in 4ms                    (no dialog)
```

- **Consent UX**: one GTK "Share Screen" window (monitor picker + "Remember" handled through `persist_mode=2`). With the restore token the dialog is skipped and `Start` takes ~4 ms. Tokens are single-use: every `Start` returns a new one that must replace the stored token. The spike stores it with mode 0600 and never prints it (it is a capture grant). On request timeout the spike calls `Request.Close`, which dismisses the dialog.
- **Formats niri offers** (`pw-dump` of the niri node): only `BGRx` DMA-BUF with NVIDIA block-linear modifiers (`0x0300000000606010…`) plus `DRM_FORMAT_MOD_INVALID`; **no LINEAR, no SHM/MemFd**.
  - Offering LINEAR only → `no more input formats`.
  - Offering IMPLICIT (`-implicit-modifier`) → negotiates and streams (~150 buffers/s), but every buffer is flagged `SPA_CHUNK_FLAG_CORRUPTED`, i.e. niri cannot render into implicit-modifier buffers on this NVIDIA setup.
  - With `SPA_POD_PROP_FLAG_DONT_FIXATE` on our modifier property the stream stayed `paused`; without it negotiation completes. We only list values we can consume, so it is not needed.
  - niri sets `chunk->size = 0` on DMA-BUF chunks; the consumer must not treat that as an empty frame.
- **Consumer path verified** against a GStreamer test source on the default daemon (`portals pwgrab`, `videotestsrc ! pipewiresink`): 640x480 BGRx MemFd, first frame 14–26 ms, colours correct. So the cgo PipeWire code, format negotiation for SHM and BGRx→RGBA conversion work; only GPU-tiled DMA-BUF is missing.
- **TODO (capture backend)**: import DMA-BUFs with the negotiated vendor modifier through EGL (`EGL_EXT_image_dma_buf_import_modifiers`) and read back with `glReadPixels` (or Vulkan), inside `internal/platform/linuxwayland` with cgo. Required for niri and KWin on NVIDIA, and generally for any producer without SHM. Until then, on such systems use the Screenshot portal.
- **Latency**: portal handshake without dialog (token): ~10 ms total to an open PipeWire remote. First frame on a SHM producer: ~15–25 ms. First frame on niri/NVIDIA: not achievable without the GPU import.

### Screenshot (explicit fallback)

```
$ portals screenshot
Screenshot ok in 2.716s: 2560x1080 saved /tmp/openclick-oc6-*/screenshot.png (portal file removed)
Screenshot ok in 283ms ...
Screenshot ok in 222ms ...
```

- `interactive=false` returned without any dialog because the permission store already has `screenshot/screenshot: "" → yes` (`""` is the app id of every unsandboxed host app). On a clean profile xdg-desktop-portal ≥ 1.19 asks once ("Allow … to take screenshots?"). For a stable per-app grant the app should register its id with `org.freedesktop.host.portal.Registry.Register` (portal ≥ 1.19) before the first request.
- The backend writes the PNG to the user's Pictures directory; the spike copies it and deletes the original so screen content does not linger (FR-SCREEN-006).
- No region or display selection in non-interactive mode: always the whole desktop. Cropping per display uses the topology from `internal/platform/linuxwayland/displays`.
- ~250 ms warm per capture is fine for on-request screenshots (FR-SCREEN-001), not for continuous capture.

### RemoteDesktop / libei

```
$ portals remotedesktop -timeout 10s
RemoteDesktop version=2 (ConnectToEIS needs >=2)
AvailableDeviceTypes=0 (1=keyboard 2=pointer 4=touch)
portals remotedesktop: CreateSession: portal: request failed
```

niri does not provide `org.gnome.Mutter.RemoteDesktop`, so the GNOME backend reports no device types and refuses sessions. libei is installed (1.6.0) but there is no EIS server to connect to. Input injection on niri therefore needs another mechanism: niri exposes `zwlr_virtual_pointer_manager_v1` and `zwp_virtual_keyboard_manager_v1` to every client (used by the OC-5 self-test), which our pure-Go Wayland client can drive — but without any consent step, so product must decide whether that is acceptable (explicit user opt-in in Settings).

## Expectations on GNOME and KDE (not tested here)

| | GNOME 48+ (Mutter, x-d-p-gnome) | KDE Plasma 6 (KWin, x-d-p-kde) |
|---|---|---|
| GlobalShortcuts | yes; dialog to confirm/assign; release events to verify | yes; KDE dialog; `Deactivated` on release |
| ScreenCast | yes; "Share Screen" dialog; restore tokens (`persist_mode=2`); Mutter offers SHM/MemFd as well as DMA-BUF → current cgo grab works | yes; KDE picker; restore tokens; KWin prefers DMA-BUF, SHM fallback exists → works with LINEAR/SHM, GPU import needed on some setups |
| Screenshot | yes; one-time permission | yes; KDE may show an interactive dialog even for `interactive=false` on older versions |
| RemoteDesktop | yes; "Remote Desktop" dialog (allow keyboard/pointer); restore tokens since v2; `ConnectToEIS` supported (GNOME ≥ 45) | yes; KDE dialog; `ConnectToEIS` since Plasma 6.1 |

## Commands the user must run (consent dialogs)

All from the repo root; each prints timings and exits.

```sh
go build -o bin/portals ./cmd/spikes/portals

# Facts only, no dialogs
bin/portals info

# 1. ScreenCast first run: a "Share Screen" window appears -> pick the monitor and click Share.
#    Saves a restore token to ~/.cache/openclick-spike/screencast.token
bin/portals screencast -no-token -implicit-modifier
# 2. Same again: no dialog expected (restore token); compare the Start time
bin/portals screencast -implicit-modifier
#    (On niri/NVIDIA both fail at the frame grab with "corrupted" buffers - expected, see above.
#     On GNOME/KDE or an Intel/AMD GPU a PNG path is printed.)

# Screenshot fallback (dialog only on a fresh permission store)
bin/portals screenshot

# On GNOME/KDE/Hyprland only (fails immediately on niri):
bin/portals shortcuts -trigger 'CTRL+ALT+space' -for 60s   # hold and release the shortcut several times
bin/portals remotedesktop                                    # allow pointer+keyboard; cursor moves 20 px right
```

To revoke what the spike was granted: `rm -r ~/.cache/openclick-spike` (stored restore tokens) and list/delete the portal entries with `busctl --user call org.freedesktop.impl.portal.PermissionStore /org/freedesktop/impl/portal/PermissionStore org.freedesktop.impl.portal.PermissionStore List s screencast` / `... DeletePermission sss screencast <id> ''`.

## Consequences for the ADR

- Wayland "Hotkeys": GlobalShortcuts portal is right for GNOME/KDE/Hyprland, but niri/Sway need the "compositor keybinding → IPC toggle" fallback; hold-to-talk only where `Deactivated` is observed.
- Wayland "Capture": ScreenCast portal + PipeWire stays primary (consent once, restore token, fast), but the capture backend needs an EGL DMA-BUF import path; Screenshot portal is the documented fallback and works on niri today.
- Wayland "Input": RemoteDesktop/libei works only on GNOME/KDE. On niri/wlroots there is no portal input; virtual-pointer/keyboard protocols are possible but consent-less.

## Open issues

- GlobalShortcuts release events unverified (no backend on niri): needs a GNOME 48 and a KDE 6 run.
- DMA-BUF import (EGL/GLES readback) for tiled NVIDIA/AMD modifiers.
- ScreenCast multi-monitor (`multiple=true`) and per-stream `position/size` mapping to `DisplayID` not exercised (single output).
- RemoteDesktop + ConnectToEIS untested end-to-end (needs GNOME/KDE); the spike only obtains the EIS fd, no libei client yet.
- App-id registration (`org.freedesktop.host.portal.Registry`) so permissions are per app, not shared by all host apps (`""`).
