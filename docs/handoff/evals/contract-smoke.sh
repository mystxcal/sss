#!/usr/bin/env bash
set -euo pipefail

: "${SSS_URL:?Set SSS_URL}"
: "${SSS_PASSWORD:?Set SSS_PASSWORD}"
SSS_USER="${SSS_USER:-sss}"
BASE="${SSS_URL%/}"
AUTH=(-u "${SSS_USER}:${SSS_PASSWORD}")

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

printf 'alpha-%s\n' "$(date +%s)" > "$tmp/alpha.txt"
printf 'beta-%s\n' "$(date +%s)" > "$tmp/beta.txt"

printf 'Checking authenticated info...\n' >&2
curl -fsS "${AUTH[@]}" "$BASE/v1/info" > "$tmp/info.json"

printf 'Checking missing authentication...\n' >&2
status="$(curl -sS -o "$tmp/noauth.txt" -w '%{http_code}' "$BASE/v1/info")"
[[ "$status" == "401" ]] || fail "expected 401 without auth, got $status"

printf 'Sending one file...\n' >&2
code="$(curl -fsS "${AUTH[@]}" \
  -F "file=@$tmp/alpha.txt" \
  -F "note=contract smoke" \
  -F "ttl=30" \
  "$BASE/s")"
code="${code//$'\r'/}"
code="${code//$'\n'/}"

[[ "$code" =~ ^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$ ]] ||
  fail "invalid code format: $code"

printf 'Inspecting %s...\n' "$code" >&2
curl -fsS "${AUTH[@]}" \
  -H "Accept: application/json" \
  "$BASE/v1/transfers/$code" > "$tmp/meta.json"
grep -q 'contract smoke' "$tmp/meta.json" ||
  fail "note missing from metadata"

printf 'Receiving one file...\n' >&2
curl -fsS "${AUTH[@]}" \
  -o "$tmp/alpha.out" \
  "$BASE/r/$code"
cmp "$tmp/alpha.txt" "$tmp/alpha.out" ||
  fail "single-file bytes differ"

printf 'Receiving same code a second time...\n' >&2
lower_code="$(printf '%s' "$code" | tr '[:upper:]' '[:lower:]')"
curl -fsS "${AUTH[@]}" \
  -o "$tmp/alpha.out.2" \
  "$BASE/r/$lower_code"
cmp "$tmp/alpha.txt" "$tmp/alpha.out.2" ||
  fail "second receive bytes differ"

printf 'Sending multiple files...\n' >&2
multi_code="$(curl -fsS "${AUTH[@]}" \
  -F "file=@$tmp/alpha.txt" \
  -F "file=@$tmp/beta.txt" \
  "$BASE/s")"
multi_code="${multi_code//$'\r'/}"
multi_code="${multi_code//$'\n'/}"

curl -fsS "${AUTH[@]}" \
  -o "$tmp/multi.zip" \
  "$BASE/r/$multi_code"

if command -v unzip >/dev/null 2>&1; then
  mkdir "$tmp/unpacked"
  unzip -q "$tmp/multi.zip" -d "$tmp/unpacked"
  cmp "$tmp/alpha.txt" "$tmp/unpacked/alpha.txt" ||
    fail "multi alpha bytes differ"
  cmp "$tmp/beta.txt" "$tmp/unpacked/beta.txt" ||
    fail "multi beta bytes differ"
fi

printf 'PASS: contract smoke completed for %s and %s\n' \
  "$code" "$multi_code"
