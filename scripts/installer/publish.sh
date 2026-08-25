#!/bin/sh
# Publish the sss client installer as an sss handoff, and print the
# one-command install line for each platform.
#
#   SSS_PASSWORD='...' sh scripts/installer/publish.sh
#
# The handoff carries install.sh, the linux/darwin binaries, and the
# checksums. TTL defaults to 50h, the server's max_ttl_minutes ceiling.
set -eu

REPO=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
DIST="$REPO/dist"
TTL="${TTL:-50h}"
# The live relay is deliberately not in this repository. Take it from the
# environment, else from the gitignored local notes.
URL="${SSS_URL:-}"
if [ -z "$URL" ] && [ -r "$REPO/DEPLOYMENT.local.md" ]; then
	URL=$(sed -n 's|.*`\(https://[^`]*\)`.*|\1|p' "$REPO/DEPLOYMENT.local.md" | head -1)
fi
if [ -z "$URL" ]; then
	echo "set SSS_URL, or record the relay in DEPLOYMENT.local.md" >&2
	exit 64
fi

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
PAYLOAD="$STAGE/sss-install"
mkdir -p "$PAYLOAD"

sed "s|__SSS_URL__|$URL|g" "$REPO/scripts/installer/install.sh"  > "$PAYLOAD/install.sh"
sed "s|__SSS_URL__|$URL|g" "$REPO/scripts/installer/install.ps1" > "$PAYLOAD/install.ps1"
chmod 0755 "$PAYLOAD/install.sh"
cp "$DIST/SHA256SUMS.txt" "$PAYLOAD/"
# Only the sss-* binaries: both installers create the sssend/ssrecv aliases
# themselves, so shipping the pre-named copies would double the payload.
for f in sss-1.0.0-linux-amd64 sss-1.0.0-linux-arm64 sss-1.0.0-darwin-arm64 \
         sss-1.0.0-windows-amd64.exe sss-1.0.0-windows-arm64.exe; do
	cp "$DIST/$f" "$PAYLOAD/"
done

CODE=$(sssend "$PAYLOAD" --note "sss client installer $(date -u +%Y-%m-%d)" --ttl "$TTL" 2>/dev/null)

cat <<EOF
code: $CODE   (valid $TTL)

Linux / macOS — one command:

  curl -fsS -u sss "$URL/r/$CODE?format=tar" | tar -x -C /tmp && sh /tmp/sss-install/install.sh

Windows (PowerShell) — one line:

  curl.exe -fsS -u sss "$URL/r/$CODE?format=tar" -o \$env:TEMP\sss.tar; tar.exe -xf \$env:TEMP\sss.tar -C \$env:TEMP; powershell -ExecutionPolicy Bypass -File \$env:TEMP\sss-install\install.ps1
EOF
