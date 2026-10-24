#!/bin/sh
# Companion installer: downloads a release from GitHub, verifies it and
# installs it for the current user.
#
#   curl -fsSL https://raw.githubusercontent.com/xlsft/companion/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/xlsft/companion/main/install.sh | sh -s -- --uninstall
#
# Environment:
#   VERSION             release to install, e.g. 0.1.0 or v0.1.0 (default: latest)
#   PREFIX              install prefix (default: ~/.local)
#   GITHUB_TOKEN        token for the GitHub API (rate limits, private forks)
#   REQUIRE_SIGNATURE=1 fail unless cosign verifies the release signature
#   COMPANION_REPO      owner/name to install from (default: xlsft/companion)
#   GITHUB_API          API base URL (default: https://api.github.com)
#
# Running it again upgrades in place; settings and models in
# ~/.config/openclick are never touched.
set -eu

REPO=${COMPANION_REPO:-xlsft/companion}
API=${GITHUB_API:-https://api.github.com}
PREFIX=${PREFIX:-$HOME/.local}
VERSION=${VERSION:-latest}
# Must match the identity release.yml signs with (README, "Verify a release").
CERT_IDENTITY="^https://github.com/$REPO/.github/workflows/release.yml@refs/tags/v.*\$"
CERT_ISSUER=https://token.actions.githubusercontent.com

if [ -t 1 ]; then
	BOLD=$(printf '\033[1m') DIM=$(printf '\033[2m') RESET=$(printf '\033[0m')
	VIOLET=$(printf '\033[1;38;2;139;92;246m') GREEN=$(printf '\033[1;32m') YELLOW=$(printf '\033[1;33m') RED=$(printf '\033[1;31m')
else
	BOLD='' DIM='' RESET='' VIOLET='' GREEN='' YELLOW='' RED=''
fi
say() { printf '%s\n' "$*"; }
ok() { say "${GREEN}✓${RESET} $*"; }
warn() { say "${YELLOW}!${RESET} $*" >&2; }
die() {
	say "${RED}✗${RESET} $*" >&2
	exit 1
}

usage() {
	sed -n '2,17p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//' || true
	say "Options: --uninstall  remove Companion from \$PREFIX"
	say "         --help       show this help"
}

uninstall=0
for arg in "$@"; do
	case $arg in
	--uninstall | uninstall) uninstall=1 ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown option $arg (try --help)" ;;
	esac
done

# Files placed by packaging/install.sh from the tarball.
installed_files() {
	printf '%s\n' \
		"$PREFIX/share/applications/companion.desktop" \
		"$PREFIX/share/metainfo/companion.metainfo.xml" \
		"$PREFIX/share/icons/hicolor/scalable/apps/companion.svg" \
		"$PREFIX/share/icons/hicolor/256x256/apps/companion.png"
}

refresh_caches() {
	if command -v update-desktop-database >/dev/null 2>&1; then
		update-desktop-database -q "$PREFIX/share/applications" 2>/dev/null || true
	fi
	if command -v gtk-update-icon-cache >/dev/null 2>&1 && [ -f "$PREFIX/share/icons/hicolor/index.theme" ]; then
		gtk-update-icon-cache -q -t "$PREFIX/share/icons/hicolor" 2>/dev/null || true
	fi
}

if [ "$uninstall" = 1 ]; then
	[ -e "$PREFIX/lib/companion/companion" ] || die "Companion is not installed in $PREFIX"
	rm -rf "${PREFIX:?}/lib/companion"
	# Only our own symlink, never a companion binary from somewhere else.
	if [ -L "$PREFIX/bin/companion" ]; then
		case $(readlink "$PREFIX/bin/companion") in
		*/lib/companion/companion) rm -f "$PREFIX/bin/companion" ;;
		esac
	fi
	installed_files | while IFS= read -r f; do rm -f "$f"; done
	refresh_caches
	ok "Removed Companion from $PREFIX"
	say "${DIM}Settings, history and speech models stay in ~/.config/openclick; delete that directory to remove them too.${RESET}"
	exit 0
fi

say ""
say "${VIOLET}↖ companion${RESET} ${BOLD}installer${RESET}"
say ""

case $(uname -s) in
Linux) ;;
Darwin) die "macOS builds are not available yet; see https://github.com/$REPO#platform-support" ;;
*) die "Unsupported OS $(uname -s); see https://github.com/$REPO#platform-support" ;;
esac
case $(uname -m) in
x86_64 | amd64) ARCH=x86_64 ;;
aarch64 | arm64) ARCH=aarch64 ;;
*) die "Unsupported architecture $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	die "sha256sum or shasum is required"
fi

# api URL [ACCEPT]: GET from the GitHub API, with the token when there is one.
api() {
	if [ -n "${GITHUB_TOKEN:-}" ]; then
		curl -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" \
			-H "Accept: ${2:-application/vnd.github+json}" "$1"
	else
		curl -fsSL --retry 3 -H "Accept: ${2:-application/vnd.github+json}" "$1"
	fi
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

case $VERSION in
latest) url="$API/repos/$REPO/releases/latest" ;;
v*) url="$API/repos/$REPO/releases/tags/$VERSION" ;;
*) url="$API/repos/$REPO/releases/tags/v$VERSION" ;;
esac
api "$url" >"$TMP/release.json" ||
	die "No release found at $url${GITHUB_TOKEN:+ (with GITHUB_TOKEN)}"

TAG=$(sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' "$TMP/release.json" | head -n 1)
[ -n "$TAG" ] || die "Could not read the release tag from the GitHub API"
VER=${TAG#v}
ARCHIVE="companion-$VER-linux-$ARCH.tar.gz"

# asset_url NAME: download URL of a release asset. With a token the API URL
# is used, which also works for private repositories.
asset_url() {
	awk -v want="$1" -v api="${GITHUB_TOKEN:+1}" '
		/"url": *".*\/releases\/assets\/[0-9]/ { u = $0; sub(/.*"url": *"/, "", u); sub(/".*/, "", u) }
		/"name": *"/ { n = $0; sub(/.*"name": *"/, "", n); sub(/".*/, "", n) }
		/"browser_download_url": *"/ {
			b = $0; sub(/.*"browser_download_url": *"/, "", b); sub(/".*/, "", b)
			if (n == want) { print (api ? u : b); exit }
		}' "$TMP/release.json"
}

fetch() { # fetch NAME: download a release asset into $TMP
	u=$(asset_url "$1")
	[ -n "$u" ] || return 1
	case $u in
	"$API"/*) api "$u" application/octet-stream >"$TMP/$1" ;;
	*) curl -fsSL --retry 3 -o "$TMP/$1" "$u" ;;
	esac
}

say "${DIM}Downloading $ARCHIVE ($TAG)…${RESET}"
fetch "$ARCHIVE" || die "Release $TAG has no $ARCHIVE"
fetch checksums.txt || die "Release $TAG has no checksums.txt"

want=$(awk -v f="$ARCHIVE" '$2 == f || $2 == "*" f { print $1 }' "$TMP/checksums.txt")
[ -n "$want" ] || die "checksums.txt lists no $ARCHIVE"
[ "$want" = "$(sha256 "$TMP/$ARCHIVE")" ] || die "Checksum mismatch for $ARCHIVE; nothing was installed"
ok "SHA-256 checksum verified"

if command -v cosign >/dev/null 2>&1; then
	fetch checksums.txt.sigstore.json || die "Release $TAG has no checksums.txt.sigstore.json"
	cosign verify-blob "$TMP/checksums.txt" --bundle "$TMP/checksums.txt.sigstore.json" \
		--certificate-identity-regexp "$CERT_IDENTITY" \
		--certificate-oidc-issuer "$CERT_ISSUER" >/dev/null 2>&1 ||
		die "Signature verification of checksums.txt failed; nothing was installed"
	ok "Sigstore signature verified (built by $REPO's release workflow)"
elif [ "${REQUIRE_SIGNATURE:-}" = 1 ]; then
	die "REQUIRE_SIGNATURE=1 but cosign is not installed (https://docs.sigstore.dev/cosign/system_config/installation/)"
else
	warn "cosign not found: only the checksum was verified, not who built the release."
	warn "Install cosign, or set REQUIRE_SIGNATURE=1 to refuse unsigned installs."
fi

previous=
if [ -x "$PREFIX/lib/companion/companion" ]; then
	previous=$("$PREFIX/lib/companion/companion" -version 2>/dev/null | head -n 1 || true)
fi

tar -xzf "$TMP/$ARCHIVE" -C "$TMP"
dir="$TMP/companion-$VER-linux-$ARCH"
[ -f "$dir/install.sh" ] || die "$ARCHIVE has an unexpected layout"
PREFIX=$PREFIX sh "$dir/install.sh" >/dev/null
refresh_caches

if [ -n "$previous" ]; then
	ok "Upgraded Companion ($previous → $TAG) in ${BOLD}$PREFIX${RESET}"
else
	ok "Installed Companion $TAG to ${BOLD}$PREFIX${RESET}"
fi

case ":$PATH:" in
*":$PREFIX/bin:"*) ;;
*)
	warn "$PREFIX/bin is not on your PATH. Add it to your shell profile:"
	say "    export PATH=\"$PREFIX/bin:\$PATH\""
	;;
esac

if [ -x "$PREFIX/lib/companion/companion" ] && command -v ldd >/dev/null 2>&1; then
	missing=$(ldd "$PREFIX/lib/companion/companion" "$PREFIX/lib/companion/stt-sidecar" 2>/dev/null | awk '/not found/ {print $1}' | sort -u | tr '\n' ' ')
	[ -z "$missing" ] || warn "Missing system libraries: $missing(see https://github.com/$REPO/blob/main/docs/linux.md#runtime-dependencies)"
fi

say ""
say "Next:"
say "  ${BOLD}companion${RESET}            start it (or pick Companion in your app menu); the setup wizard opens on first run"
say "  ${BOLD}companion -diagnose${RESET}  print what this desktop supports"
say "  Hotkey bindings, portals and dependencies: https://github.com/$REPO/blob/main/docs/linux.md"
say "  Uninstall: curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | sh -s -- --uninstall"
