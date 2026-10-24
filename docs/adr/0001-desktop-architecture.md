# ADR-0001: Desktop architecture

- Status: Accepted
- Date: 2026-10-24
- Ticket: OC-1
- Source: PRD — локальный аналог HeyClicky (YouTrack OC-A-1), §9

## Context

Companion (repo `openclick`) is a local desktop assistant for Windows, macOS and Linux. Hard constraints from the PRD:

- one codebase, as much Go as possible, UI on Gio, no WebView/Electron/TS UI;
- speech recognition is local (whisper.cpp), LLM is BYOK (OpenAI/Anthropic-compatible), TTS is an external provider;
- no backend, no accounts, all data on device;
- OS-specific functionality (hotkeys, capture, overlay, input, accessibility, secrets) differs per platform and, on Linux, per compositor.

## Decision

### Processes

| Process | Binary | Responsibility |
|---|---|---|
| Core | `cmd/companion` | Gio UI, orchestration, sessions, LLM/TTS clients, storage, platform bridges |
| STT sidecar | `cmd/stt-sidecar` | whisper.cpp runtime, VAD, partial/final decode, hardware backend |
| Browser host (P2) | `cmd/browser-host` | Chrome native messaging host |

The sidecar is isolated so that a native crash in whisper.cpp (or GPU backend init) never takes down the core (FR-STT-010, NFR-003). The core supervises it with bounded restarts (OC-36).

### IPC

- Core ↔ STT sidecar: JSON-RPC 2.0 over the sidecar's stdin/stdout, newline-delimited. Audio is sent as base64 PCM16 mono 16 kHz chunks inside notifications. No TCP port is opened.
- Later optimisation (gRPC over Unix socket / named pipe) must keep the same method set.
- stderr of the sidecar is technical log only, already redacted (no transcript text).

### Layering

```
cmd/*                 – entrypoints, flag parsing, wiring only
internal/app          – lifecycle, dependency wiring, Readiness Gate
internal/<domain>     – session, llm, stt, tts, screen, coordinates, overlay, ...
internal/platform/*   – per-OS bridges implementing pkg/contracts interfaces
pkg/contracts         – stable interfaces and value types shared across modules
```

Rules:

1. Domain packages depend on `pkg/contracts`, never on `internal/platform/*`.
2. `internal/platform/*` contain no business logic: they expose narrow OS operations (PRD §9.3).
3. Platform selection happens in `internal/platform` via build tags (`linux`, `windows`, `darwin`) and runtime detection (X11 vs Wayland, compositor, portals).
4. Every platform operation that may be missing returns `contracts.ErrUnsupported` wrapped with the capability name; callers consult the capability report first and present a fallback (PRD §4.2).
5. All long operations take `context.Context` and honour cancellation.
6. cgo is allowed only inside `internal/platform/*` and `cmd/stt-sidecar`. The rest of the core stays pure Go so it cross-compiles.

### Platform boundaries

| Area | Windows | macOS | Linux X11 | Linux Wayland |
|---|---|---|---|---|
| Hotkeys | RegisterHotKey / LL keyboard hook | CGEvent tap | XInput2/XRecord | xdg GlobalShortcuts portal |
| Capture | Windows Graphics Capture | ScreenCaptureKit | XGetImage/XShm | ScreenCast portal + PipeWire |
| Overlay | layered WS_EX_TRANSPARENT window | non-activating NSPanel | shaped override-redirect window | wlr-layer-shell; fallback panel |
| Input | SendInput | CGEvent | XTest | RemoteDesktop portal / libei |
| Accessibility | UI Automation | AXUIElement | AT-SPI2 | AT-SPI2 |
| Secrets | Credential Manager | Keychain | Secret Service | Secret Service |

Gio renders the normal windows (setup wizard, Home, Settings, Diagnostics). Overlay windows need window flags Gio does not expose (click-through, layer-shell, non-activating), so each overlay backend owns its native window and draws with a small Go 2D renderer into a shared-memory buffer; Gio is not used for the overlay surface.

### Storage

SQLite (pure Go driver `modernc.org/sqlite`, no cgo) for metadata, files in the user data directory for blobs and models, OS secret store for keys. Logs never contain prompts, transcripts, screenshots, audio, keys or tokens.

## Consequences

- Core cross-compiles for all three OSes from any host; only platform bridges and the sidecar need native toolchains.
- Each platform spike (OC-3…OC-6) validates its row of the table above before mass implementation; a failed spike changes this ADR, not the domain code.
- A whisper.cpp crash costs one request, not the app.
- Overlay drawing code is shared Go; only window creation and buffer presentation are per platform.
