# openclick

Local desktop voice/screen assistant (working name Companion). Go core, Gio UI, local whisper.cpp STT, BYOK LLM, external TTS, no backend.

- Product: PRD in YouTrack knowledge base article OC-A-1 (https://xlsft.youtrack.cloud/articles/OC-A-1).
- Tasks: YouTrack project OC (OC-1…OC-130), tags P0/P1/P2, "depends on" links.
- Architecture: `docs/adr/0001-desktop-architecture.md` — read before changing package layout.

## Layout

- `cmd/companion` core, `cmd/stt-sidecar` STT process. Entrypoints only wire things.
- `pkg/contracts` shared interfaces and value types. Changing them affects everyone: keep changes additive.
- `internal/<domain>` domain packages; they depend on `pkg/contracts`, never on `internal/platform/*`.
- `internal/platform/<os>` thin OS bridges, build-tagged; cgo only here, in `cmd/stt-sidecar` and in the Gio UI (`internal/ui`, linked by `cmd/companion`; ADR-0001 amendment 1).

## Rules

- Every long operation takes `context.Context` and honours cancellation.
- Never log prompts, transcripts, screen content, audio, API keys or tokens. Log technical codes only.
- Missing platform features return `contracts.Unsupported(cap)`; don't fake success.
- Tests: table-driven, stdlib `testing`, no network. Fake HTTP servers via `httptest`.
- `make build`, `make test`, `make vet`, `make cross` must pass before committing (cross = pure-Go code builds for windows/darwin/linux; build = native cgo build incl. UI).
- Comments explain why, not what; match the density of surrounding code.

## Git

- Atomic commits, one logical change each, message `type: summary (OC-N)`.
- Push right after every commit. Never make two commits without pushing in between.
- End commit messages with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
