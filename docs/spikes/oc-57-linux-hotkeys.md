# OC-57: Linux global hotkeys

- Date: 2026-10-24
- Code: `internal/platform/linuxx11/hotkeys` (X11 grabs), `internal/platform/linuxwayland/hotkeys` (GlobalShortcuts portal), `internal/platform/control` (control socket + `companion ctl`), `internal/platform/hotkeys` (chooser), `internal/platform/keys` (key names), `cmd/spikes/hotkeys`
- Test machine: Arch Linux, niri 26.04, xwayland-satellite on `:0` (Xwayland 24.1.14), Xvfb for X11 key injection

## Backend selection

| Session | Primary | Control socket | `SupportsRelease()` |
|---|---|---|---|
| X11 | `GrabKey` on root, every NumLock/CapsLock/ScrollLock combination | always | true |
| Wayland, portal binds | GlobalShortcuts (`BindShortcuts`, `preferred_trigger` e.g. `CTRL+ALT+space`) | always | false until the first `Deactivated` after an `Activated` |
| Wayland, portal refuses (niri) or absent (Sway) | none: dropped at the first `Register` on `Unsupported` | always | false until the first `release` command |

The socket always runs so `companion ctl` works on every session. `Service.Backend()` says which path delivers; `ipc+compositor-binding` means the user must add a compositor binding (`control.Instructions`).

Why the portal starts with `SupportsRelease()==false`: whether a backend sends `Deactivated` on key-up cannot be queried, and OC-6 could not verify it on GNOME. Wrongly claiming release would leave hold-to-talk recording forever; starting in toggle costs one session in toggle mode before the first release switches the session layer to hold-to-talk.

`contracts.HotkeyToggled` (new, additive) is what `companion ctl talk-toggle` sends; `session.FromHotkey` maps it to `ToggleTalk`, so a press-only compositor bind starts and stops recording in every voice mode.

## Results on niri

```
$ bin/hotkeys -drive bin/companion -for 3s
session=wayland compositor="niri" global_hotkey={degraded ipc+compositor-binding} hotkey_release={unavailable ... Fallback:toggle_talk}
backend xdg-portal:GlobalShortcuts opened in 1.184ms
Register in 2.463ms: err=<nil>
backend after Register: ipc+compositor-binding           (BindShortcuts failed -> Unsupported -> portal dropped)
drive: companion ctl ping         exit=<nil> in 3.455ms
event talk   toggled  SupportsRelease=false
drive: companion ctl talk-toggle  exit=<nil> in 3.548ms
event cancel pressed  SupportsRelease=false
event talk   pressed  SupportsRelease=false               (talk-press)
event talk   released SupportsRelease=true held=204ms     (talk-release flips release support)
drive: in-process Ctl talk-toggle err=<nil> in 60µs
```

- `companion ctl` round trip including process start: ~3.5 ms; the socket itself: ~60 µs.
- `bin/hotkeys -backend portal`: `BindShortcuts refused by portal backend: global_hotkey: unsupported on this platform` after 2 ms, no dialog (same as OC-6).
- The niri snippet passes `niri validate`:

```kdl
binds {
    Mod+Space repeat=false { spawn "companion" "ctl" "talk-toggle"; }
    Mod+Escape repeat=false { spawn "companion" "ctl" "cancel"; }
}
```

`repeat=false` matters: niri repeats held binds, which would toggle on and off.

## X11

```
$ DISPLAY=:97 XDG_SESSION_TYPE=x11 bin/hotkeys -xtest -for 3s      # Xvfb
Register in 126µs: err=<nil>
xtest: holding ctrl+alt+space for 1s
event talk   pressed  SupportsRelease=true
event talk   released SupportsRelease=true held=1.021s
event cancel pressed / released (escape)
```

- A second client grabbing the same chord gets `BadAccess`, reported as `hotkey talk (ctrl+alt+space): hotkey already taken by another application` (`contracts.ErrHotkeyConflict`); `OPENCLICK_X11_LIVE=1 DISPLAY=:97 go test ./internal/platform/linuxx11/hotkeys` covers grab, conflict and press/release.
- Autorepeat: xgb has no XKB binding, so detectable autorepeat is not requested. Release+press pairs with the same server timestamp are dropped, a release is delayed by 20 ms to wait for that press, and repeated presses of a held key are ignored.
- Releases arrive 20 ms late because of that delay.
- `MappingNotify` also fires whenever input switches between devices (XTEST, a second keyboard); keys are grabbed again only when the keymap really changed.
- On xwayland-satellite (`:0`), grabs succeed but XTEST-injected keys never reach them: Xwayland 24 forwards XTEST to the compositor through libei. Real keys reach the grabs only while an X client has focus. That's why the chooser never uses X11 on a Wayland session.
- XKB eats Ctrl+Alt+F1…F12 as VT switches, so those chords never reach clients.

## Open issues

- `Deactivated` on GNOME 48 / KDE 6 still unverified (no portal backend here); the backend adapts at runtime either way.
- The first hold-to-talk on Sway/Hyprland runs in toggle mode, because `SupportsRelease` flips only after the first `talk-release`.
- A bare Escape grab on X11 takes Escape from every application while the binding exists; the core should register `cancel` only while a session is active.
- Hotkeys are not yet wired into `internal/app` / `session.Manager` (no session wiring exists yet); `companion ctl` gets "not running" until they are.
- No XKB detectable autorepeat (needs raw XKB requests via xgb); the timestamp filter covers classic and detectable autorepeat.
