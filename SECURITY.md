# Security policy

## Supported versions

Only the latest release gets security fixes.

## Reporting a vulnerability

Please do not open a public issue. Report it privately through
[GitHub security advisories](https://github.com/xlsft/companion/security/advisories/new), or by email to
dev@xlsft.ru. Include the affected version (`companion -version`), what an attacker can do, and steps to reproduce.
You will get an answer within a few days; fixes are released as soon as they are ready and credited unless you prefer
otherwise.

Especially relevant: anything that sends screen content, audio, transcripts or keys somewhere other than the endpoints
the user configured, writes them to disk or logs against the settings, or lets another local user drive the control
socket (`$XDG_RUNTIME_DIR/openclick/control.sock`).

## What leaves your computer

- **No backend, no account, no telemetry.** Companion talks only to the endpoints you configure.
- **Audio never leaves the computer.** Speech recognition is local; only the recognised text is sent to your model.
- **Screenshots** are taken only when you ask, one frame, and sent only to your model endpoint. By default they are kept
  in memory and never written to disk; storing them with the chat history is an opt-in setting.
- **Spoken answers**: the answer text goes to the speech provider you picked (ElevenLabs or an OpenAI-compatible
  endpoint); with answers as text, nothing does.
- **Keys** are stored in the system keychain (Secret Service), never in the database or config files.
- **Logs** contain technical codes only: no prompts, transcripts, screen content, audio or keys. The diagnostics report
  you can copy follows the same rule.

## Verifying releases

Every release has a `checksums.txt` with the SHA-256 of each asset, signed keylessly with
[Sigstore cosign](https://docs.sigstore.dev) by the release workflow of this repository. The signature bundle is
`checksums.txt.sigstore.json`. To check a download:

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/xlsft/companion/.github/workflows/release.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
```

A valid signature proves the checksums were produced by `.github/workflows/release.yml` running for a `v*` tag of
`xlsft/companion`, not by anyone holding a key. `install.sh` runs the same check when cosign is installed and only the
checksum otherwise (with a warning); `REQUIRE_SIGNATURE=1` makes the signature mandatory.
