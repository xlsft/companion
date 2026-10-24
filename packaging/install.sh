#!/bin/sh
# Installs the extracted tarball under $PREFIX (default ~/.local).
# The two binaries go into one directory because the core looks for
# stt-sidecar next to its own executable; a symlink in bin/ keeps that working
# since os.Executable resolves it.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
prefix=${PREFIX:-$HOME/.local}
lib="$prefix/lib/companion"

install -d "$lib" "$prefix/bin" \
	"$prefix/share/applications" "$prefix/share/metainfo" \
	"$prefix/share/icons/hicolor/scalable/apps" "$prefix/share/icons/hicolor/256x256/apps"
install -m 755 "$here/companion" "$here/stt-sidecar" "$lib/"
# sherpa-onnx's libraries are found next to the sidecar ($ORIGIN).
install -m 644 "$here/libsherpa-onnx-c-api.so" "$here/libonnxruntime.so" "$lib/"
ln -sf "$lib/companion" "$prefix/bin/companion"
install -m 644 "$here/share/metainfo/companion.metainfo.xml" "$prefix/share/metainfo/"
install -m 644 "$here/share/icons/hicolor/scalable/apps/companion.svg" "$prefix/share/icons/hicolor/scalable/apps/"
install -m 644 "$here/share/icons/hicolor/256x256/apps/companion.png" "$prefix/share/icons/hicolor/256x256/apps/"
# The entry goes last and appears in one rename: launchers load it the
# moment it shows up and remember an icon they could not find yet. They
# also start it with the session's PATH, which often lacks ~/.local/bin, so
# it names the binary by its full path.
entry="$prefix/share/applications/companion.desktop"
sed "s|^Exec=companion\$|Exec=\"$prefix/bin/companion\"|" "$here/share/applications/companion.desktop" >"$entry.new"
chmod 644 "$entry.new"
mv -f "$entry.new" "$entry"
echo "installed to $prefix (make sure $prefix/bin is on PATH)"
