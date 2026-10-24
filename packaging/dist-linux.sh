#!/bin/sh
# Packs bin/companion, bin/stt-sidecar and the sherpa-onnx libraries next to
# it (built by `make dist-linux`) into
# dist/companion-VERSION-linux-ARCH.tar.gz and, when appimagetool is usable,
# dist/Companion-VERSION-ARCH.AppImage.
#
# usage: dist-linux.sh VERSION
#   APPIMAGE=required|auto|skip   (default required: fail without appimagetool)
set -eu

version=${1:?usage: dist-linux.sh VERSION}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
arch=$(uname -m)
name="companion-$version-linux-$arch"
dist="$root/dist"
mode=${APPIMAGE:-required}

# Pinned: a moving "continuous" release would break the checksum.
AIT_VERSION=1.9.1
# shellcheck disable=SC2034 # read through eval below, by arch name
AIT_SHA256_x86_64=ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0
# shellcheck disable=SC2034
AIT_SHA256_aarch64=f0837e7448a0c1e4e650a93bb3e85802546e60654ef287576f46c71c126a9158

die() { echo "dist-linux: $*" >&2; exit 1; }

for f in bin/companion bin/stt-sidecar; do
	[ -x "$f" ] || die "$f missing; run make dist-linux, not this script directly"
done
# The fake-engine sidecar from plain `make build` would ship without speech
# recognition; the release build links both engines (make build-stt).
grep -q whisper_init_from_file "$root/bin/stt-sidecar" ||
	die "bin/stt-sidecar lacks the whisper engine (run: make whisper build-stt)"
grep -q SherpaOnnxCreateOfflineRecognizer "$root/bin/stt-sidecar" ||
	die "bin/stt-sidecar lacks the sherpa-onnx engine (run: make build-stt)"
# sherpa-onnx's prebuilt libraries are loaded from next to the sidecar
# ($ORIGIN runpath), see docs/stt-protocol.md#sherpa-onnx-build.
sherpa_libs="bin/libsherpa-onnx-c-api.so bin/libonnxruntime.so"
for f in $sherpa_libs; do
	[ -f "$f" ] || die "$f missing (run: make build-stt)"
done

# sidecar DIR: installs the sidecar and its libraries into DIR. The build
# host's module-cache runpath is dropped when patchelf is available; left
# in, it is only a dead search path after $ORIGIN.
sidecar() {
	install -m 755 bin/stt-sidecar "$1/"
	# shellcheck disable=SC2086 # sherpa_libs is a list of paths without spaces
	install -m 644 $sherpa_libs "$1/"
	if command -v patchelf >/dev/null; then
		# shellcheck disable=SC2016 # $ORIGIN is for the dynamic loader, not the shell
		patchelf --set-rpath '$ORIGIN' "$1/stt-sidecar"
	fi
}

# --- directory tree shared by tarball and AppDir --------------------------
stage() { # stage DIR: fills DIR with the files common to both formats
	d=$1
	install -d "$d/share/applications" "$d/share/metainfo" "$d/licenses" \
		"$d/share/icons/hicolor/scalable/apps" "$d/share/icons/hicolor/256x256/apps"
	install -m 644 packaging/companion.desktop "$d/share/applications/"
	install -m 644 packaging/companion.metainfo.xml "$d/share/metainfo/"
	install -m 644 packaging/companion.svg "$d/share/icons/hicolor/scalable/apps/"
	install -m 644 packaging/companion.png "$d/share/icons/hicolor/256x256/apps/"
	install -m 644 LICENSE.md "$d/LICENSE.md"
	install -m 644 packaging/NOTICE "$d/NOTICE"
	install -m 644 internal/assets/fonts/OFL.txt "$d/licenses/OFL.txt"
	install -m 644 internal/ui/icon/LICENSE.mingcute "$d/licenses/LICENSE.mingcute"
	install -m 644 packaging/licenses/LICENSE.sherpa-onnx packaging/licenses/LICENSE.onnxruntime "$d/licenses/"
}

rm -rf "${dist:?}/$name" "$dist/AppDir"
mkdir -p "$dist"

# --- tarball -----------------------------------------------------------------
t="$dist/$name"
install -d "$t"
install -m 755 bin/companion "$t/"
sidecar "$t"
install -m 755 packaging/install.sh "$t/"
install -m 644 packaging/README.txt "$t/"
stage "$t"
# Fixed owner/mtime-independent ordering keeps the archive reproducible enough.
tar --sort=name --owner=0 --group=0 --numeric-owner -C "$dist" -czf "$dist/$name.tar.gz" "$name"
rm -rf "$t"
echo "dist-linux: $dist/$name.tar.gz"

# --- AppImage ----------------------------------------------------------------
[ "$mode" = skip ] && exit 0

command -v mksquashfs >/dev/null || die "mksquashfs missing (package squashfs-tools); install it or use APPIMAGE=skip"

tool=$(command -v appimagetool || true)
if [ -z "$tool" ]; then
	eval "want=\${AIT_SHA256_$arch:-}"
	[ -n "$want" ] || die "no appimagetool for $arch; install it or use APPIMAGE=skip"
	scratch="$dist/.tools"
	tool="$scratch/appimagetool-$AIT_VERSION-$arch.AppImage"
	if [ ! -x "$tool" ]; then
		command -v curl >/dev/null || die "appimagetool not found and curl missing; install appimagetool from https://github.com/AppImage/appimagetool"
		mkdir -p "$scratch"
		curl -fsSL -o "$tool.part" \
			"https://github.com/AppImage/appimagetool/releases/download/$AIT_VERSION/appimagetool-$arch.AppImage" ||
			die "downloading appimagetool failed; install it manually or use APPIMAGE=skip"
		echo "$want  $tool.part" | sha256sum -c - >/dev/null ||
			{ rm -f "$tool.part"; die "appimagetool checksum mismatch, refusing to run it"; }
		chmod +x "$tool.part"
		mv "$tool.part" "$tool"
	fi
fi

a="$dist/AppDir"
install -d "$a/usr/bin"
install -m 755 bin/companion "$a/usr/bin/"
sidecar "$a/usr/bin"
stage "$a/usr"
# AppImage wants the desktop entry and icon at the AppDir root too. AppRun is
# a symlink so /proc/self/exe resolves into usr/bin, where the sidecar is.
cp packaging/companion.desktop "$a/companion.desktop"
cp packaging/companion.png "$a/companion.png"
ln -s usr/bin/companion "$a/AppRun"

out="$dist/Companion-$version-$arch.AppImage"
# --appimage-extract-and-run: no FUSE needed on the build host.
ARCH=$arch "$tool" --appimage-extract-and-run --no-appstream "$a" "$out" >/dev/null ||
	die "appimagetool failed"
rm -rf "$a"
echo "dist-linux: $out"
