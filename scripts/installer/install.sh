#!/bin/sh
# sss client installer — extracted from an sss handoff, run in place.
#
#   curl -fsS -u sss "https://YOUR-RELAY/r/CODE?format=tar" \
#     | tar -x -C /tmp && sh /tmp/sss-install/install.sh
#
# Installs the right binary for this machine, creates the sssend/ssrecv
# aliases, and points the client at the relay.
set -eu

# __SSS_URL__ is stamped with the real relay by publish.sh.
SSS_URL="${SSS_URL:-__SSS_URL__}"
VERSION="1.0.0"

# The payload directory is wherever this script was extracted to.
SRC=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

case "$(uname -s)" in
	Linux)  os=linux ;;
	Darwin) os=darwin ;;
	*) echo "unsupported OS: $(uname -s); use the .exe on Windows" >&2; exit 2 ;;
esac

case "$(uname -m)" in
	x86_64|amd64)  arch=amd64 ;;
	arm64|aarch64) arch=arm64 ;;
	*) echo "unsupported architecture: $(uname -m)" >&2; exit 2 ;;
esac

BIN="$SRC/sss-$VERSION-$os-$arch"
[ -f "$BIN" ] || { echo "no binary for $os/$arch in this handoff" >&2; exit 2; }

# Verify against the checksums shipped alongside, when a hasher is present.
if [ -f "$SRC/SHA256SUMS.txt" ]; then
	want=$(awk -v f="./sss-$VERSION-$os-$arch" '$2 == f { print $1 }' "$SRC/SHA256SUMS.txt")
	if command -v sha256sum >/dev/null 2>&1; then
		got=$(sha256sum "$BIN" | cut -d' ' -f1)
	elif command -v shasum >/dev/null 2>&1; then
		got=$(shasum -a 256 "$BIN" | cut -d' ' -f1)
	else
		got=""
	fi
	if [ -n "$got" ] && [ -n "$want" ] && [ "$got" != "$want" ]; then
		echo "checksum mismatch for $BIN" >&2
		exit 6
	fi
fi

# Prefer /usr/local/bin, fall back to ~/.local/bin when we are not root.
if [ -n "${SSS_INSTALL_DIR:-}" ]; then
	DEST="$SSS_INSTALL_DIR"
	SUDO=""
	mkdir -p "$DEST"
elif [ "$(id -u)" = 0 ]; then
	DEST=/usr/local/bin
	SUDO=""
elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
	DEST=/usr/local/bin
	SUDO=sudo
else
	DEST="$HOME/.local/bin"
	SUDO=""
	mkdir -p "$DEST"
fi

$SUDO install -m 0755 "$BIN" "$DEST/sss"
$SUDO ln -sf "$DEST/sss" "$DEST/sssend"
$SUDO ln -sf "$DEST/sss" "$DEST/ssrecv"

# macOS quarantines anything that arrived over the network.
[ "$os" = darwin ] && xattr -d com.apple.quarantine "$DEST/sss" 2>/dev/null || true

"$DEST/sss" configure --url "$SSS_URL"

echo "installed $("$DEST/sss" version | head -1) to $DEST"
case ":$PATH:" in
	*":$DEST:"*) ;;
	*) echo "note: $DEST is not on your PATH — add it to your shell profile" ;;
esac
echo "next: export SSS_PASSWORD='...' && sss doctor"
