Companion (Linux)

Layout
  companion       the app (Gio UI)
  stt-sidecar     speech recognition process; must stay next to companion
  share/          desktop entry, icon, AppStream metainfo
  licenses/       third-party licences, see NOTICE
  install.sh      copies everything under a prefix (default ~/.local)

Run in place
  ./companion

Install for the current user
  ./install.sh              # to ~/.local
  PREFIX=/usr ./install.sh  # system-wide, as root

Binaries are dynamically linked against the system graphics stack (EGL,
Wayland, X11, xkbcommon) and libstdc++/libgomp for the sidecar. Runtime
dependencies, portals and per-desktop capabilities: docs/linux.md in the
source tree, or https://github.com/xlsft/companion once published.

No speech model is included. Companion downloads one on first run into
~/.config/openclick.
