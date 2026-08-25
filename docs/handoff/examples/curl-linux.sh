#!/usr/bin/env bash
set -euo pipefail

: "${SSS_URL:?Set SSS_URL, for example https://drop.example.com}"
SSS_USER="${SSS_USER:-sss}"

# For unattended use, set SSS_PASSWORD or replace AUTH with --netrc.
: "${SSS_PASSWORD:?Set SSS_PASSWORD or adapt this script to use --netrc}"
AUTH=(-u "${SSS_USER}:${SSS_PASSWORD}")

send_one() {
  local file="$1"
  curl -fsS "${AUTH[@]}" \
    -F "file=@${file}" \
    "${SSS_URL%/}/s"
}

send_many() {
  curl -fsS "${AUTH[@]}" \
    -F "file=@report.pdf" \
    -F "file=@results.csv" \
    -F "note=Review these results." \
    -F "ttl=120" \
    "${SSS_URL%/}/s"
}

receive_auto() {
  local code="$1"
  curl -fS "${AUTH[@]}" -OJ \
    "${SSS_URL%/}/r/${code}"
}

inspect_json() {
  local code="$1"
  curl -fsS "${AUTH[@]}" \
    -H "Accept: application/json" \
    "${SSS_URL%/}/v1/transfers/${code}"
}

send_directory_stream() {
  local directory="$1"
  local name="${2:-directory.tar.gz}"
  tar -C "$directory" -czf - . |
    curl -fsS "${AUTH[@]}" \
      -H "X-SSS-Filename: ${name}" \
      -H "X-SSS-TTL: 120" \
      --data-binary @- \
      "${SSS_URL%/}/s/raw"
}

local_vps_path() {
  local code="$1"
  curl -fsS \
    --unix-socket /run/sss/sssd.sock \
    "http://localhost/local/r/${code}"
}

cat <<'USAGE'
Loaded example functions:
  send_one FILE
  send_many
  receive_auto CODE
  inspect_json CODE
  send_directory_stream DIRECTORY [ARCHIVE_NAME]
  local_vps_path CODE
USAGE
