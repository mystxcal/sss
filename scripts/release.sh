#!/usr/bin/env bash
# Build the release artifacts: cross-compiled binaries, alias shims, checksums,
# and a manifest describing exactly how they were produced.
set -euo pipefail

VERSION="${1:?usage: release.sh VERSION}"
DIST="${DIST:-dist}"
cd "$(dirname "$0")/.."

if [[ ! -d "$DIST" ]]; then
  printf 'release.sh: %s does not exist; run "make cross" first\n' "$DIST" >&2
  exit 1
fi

# The Windows client is expected to be invoked as sssend/ssrecv too. The binary
# dispatches on its own name, so the aliases are plain copies.
for arch in amd64 arm64; do
  base="$DIST/sss-${VERSION}-windows-${arch}.exe"
  [[ -f "$base" ]] || continue
  cp "$base" "$DIST/sssend-${VERSION}-windows-${arch}.exe"
  cp "$base" "$DIST/ssrecv-${VERSION}-windows-${arch}.exe"
done

# Linux and macOS installs use symlinks created at install time, so no extra
# binaries are shipped for them.

pushd "$DIST" >/dev/null
sha256sum ./* > SHA256SUMS.txt
popd >/dev/null

go_version="$(go version | awk '{print $3}')"
commit="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cat > "$DIST/RELEASE_MANIFEST.json" <<EOF
{
  "product": "sss",
  "version": "${VERSION}",
  "commit": "${commit}",
  "built_at": "${built_at}",
  "go_version": "${go_version}",
  "cgo_enabled": false,
  "protocol_version": "1.0",
  "manifest_schema_version": 1,
  "reproduce": "CGO_ENABLED=0 make release VERSION=${VERSION}",
  "checksums": "SHA256SUMS.txt"
}
EOF

printf 'release artifacts in %s:\n' "$DIST"
ls -1 "$DIST"
