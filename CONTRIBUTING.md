# Contributing

Thanks for helping. Bug reports with a `companion -diagnose` report, small focused pull requests and platform knowledge
(compositors, portals, distributions) are the most useful contributions.

## Contributor License Agreement

Before a pull request can be merged, its author signs the [CLA](CLA.md) once: the CLA bot comments on your first pull
request, and you sign by replying with the sentence it quotes. Maintainers and bots are exempt.

## Development setup

See [Building from source](docs/development.md#building-from-source). The usual loop:

```sh
make vet test cross     # must pass before every commit
make build              # native build including the Gio UI
golangci-lint run       # CI runs it with .golangci.yml
```

Speech recognition needs `make whisper` once and `make build-whisper`; without it `bin/stt-sidecar` is a fake engine,
which is enough for most UI and core work.

## Ground rules

These come from [CLAUDE.md](CLAUDE.md) and [ADR-0001](docs/adr/0001-desktop-architecture.md); read the ADR before moving
packages around.

- `cmd/*` only wires things; domain logic lives in `internal/<domain>` and depends on `pkg/contracts`, never on
  `internal/platform/*`.
- `pkg/contracts` is shared by everyone: keep changes additive.
- cgo only in `internal/platform/*`, `cmd/stt-sidecar`, the whisper engine and the Gio UI (`internal/ui`, `cmd/companion`).
  Everything else must build with `CGO_ENABLED=0` for Windows, macOS and Linux (`make cross`).
- Every long operation takes a `context.Context` and honours cancellation.
- A missing platform feature returns `contracts.Unsupported(cap)`; never fake success.
- **Never log** prompts, transcripts, screen content, audio, API keys or tokens. Log technical codes only.
- Tests are table-driven, stdlib `testing`, no network; fake HTTP servers with `httptest`.
- Comments explain why, not what.

## Commits and pull requests

- One logical change per commit, message `type: summary`, types from
  [Conventional Commits](https://www.conventionalcommits.org) (`feat`, `fix`, `perf`, `refactor`, `test`, `docs`, `build`,
  `ci`, `chore`, `style`). `feat`, `fix` and `perf` commits make up the release notes.
- Keep pull requests small and describe how you tested them, including which desktop or compositor.
- CI must be green: gofmt, `go mod tidy`, vet, golangci-lint, govulncheck, shellcheck, `go test -race`, native and cross
  builds, the whisper sidecar build.

## Releases

Maintainers push a `v*` tag. The Release workflow builds the Linux tarballs and AppImages natively on x86_64 and aarch64,
then GoReleaser writes `checksums.txt`, signs it with cosign and publishes the release with a changelog; the Homebrew
tap is updated from the same checksums. Changes to packaging files run the same pipeline as a dry run on every push.
