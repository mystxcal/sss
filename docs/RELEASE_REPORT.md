# Release Report — sss 1.0.0

Completion is a release, not a repository that looks busy. This report states
what was built, what was verified, and what is deliberately absent.

## Delivered

| Requirement | Where |
|---|---|
| Production server binary | `dist/sss-1.0.0-linux-amd64` (`sss serve`) |
| Windows and Linux client binaries | `dist/sss-1.0.0-{linux,windows,darwin}-{amd64,arm64}` |
| `sssend` / `ssrecv` aliases | symlinks on Unix (`make install-aliases`); `.exe` copies for Windows in `dist/` |
| Documented raw `curl` operation | [`README.md`](../README.md), [`docs/install/debian11.md`](install/debian11.md), `scripts/contract-smoke.sh` |
| Debian 11 deployment assets | [`packaging/systemd/sss.service`](../packaging/systemd/sss.service), [`packaging/caddy/Caddyfile.example`](../packaging/caddy/Caddyfile.example), [`packaging/config.example.toml`](../packaging/config.example.toml) |
| Functional local Unix-socket receive | `GET /local/r/{code}`; `sss recv` prefers it automatically |
| Resumable advanced transfers | tus-compatible `HEAD`/`PATCH` on `/v1/uploads/{id}`; ranged segment downloads |
| Deterministic lifecycle and cleanup | `internal/transfer`, `internal/janitor`, `internal/reconcile` |
| Cross-platform and fault-injection evidence | `integration/blackbox`, `integration/faults` |
| Reproducible build instructions | `Makefile`, `scripts/release.sh`, `dist/RELEASE_MANIFEST.json` |
| Release manifest and checksums | `dist/SHA256SUMS.txt`, `dist/RELEASE_MANIFEST.json` |

## Quality gates

| Gate | Status | Evidence |
|---|---|---|
| 1 — Contract fidelity | pass | `scripts/contract-smoke.sh` passes unmodified against a live server; every documented endpoint exists; CLI stdout/stderr and exit codes asserted in `integration/blackbox/cli_test.go`; error catalog asserted in `internal/protocol/protocol_test.go` |
| 2 — Data correctness | pass | byte-for-byte round trips for single files, multi-file ZIP, raw streams, and a 10,001-file tree; digest mismatch blocks commit; symlinks and unsupported entries rejected; atomic receive finalization |
| 3 — Lifecycle correctness | pass | TTL starts at commit and is asserted against the commit timestamp; default 30 / max 3000 enforced; expiry returns 410; multiple concurrent receivers; cleanup idempotent (`lifecycle_test.go`) |
| 4 — Failure recovery | pass | crash boundaries covered in `integration/faults`: interrupted publish completed idempotently, interrupted materialization failed cleanly, interrupted deletion finished, orphan directories trashed, committed record without payload dropped. Truncated uploads publish nothing (`acceptance_test.go`). Disk admission returns 507 without writing |
| 5 — Cross-platform behavior | partial | Linux native tests pass, including spaces, Unicode, and the executable bit; Windows and macOS binaries cross-compile and the path rules reject Windows-impossible names. **Native Windows test runs have not been executed** — see below |
| 6 — Operational readiness | pass | systemd unit, Caddyfile, install guide, runbook, upgrade/recovery guide; `healthz`, `readyz`, `doctor`, `admin status`, `admin cleanup` all verified against a running daemon; logs redact secrets |
| 7 — Performance and resource behavior | pass | [`docs/operations/benchmarks.md`](operations/benchmarks.md): 231 MiB across 10,001 files sent in 7 requests and received in 4, server peak RSS 47 MiB while a 200 MiB file passed through; local receive copies zero bytes (asserted in `local_test.go`) |
| 8 — Release integrity | pass | no TODOs, debug bypasses, or placeholder responses; version, commit, and build date embedded (`sss version`); `SHA256SUMS.txt` and a reproduce command in the release manifest |

## How to reproduce the verification

```bash
make check                      # go vet + full suite (unit, blackbox, faults)
make race                       # same suite under the race detector
make release VERSION=1.0.0      # binaries, alias shims, checksums, manifest

# Against a running server:
SSS_URL=https://drop.example.com SSS_PASSWORD=... scripts/contract-smoke.sh
```

## Known limitations

These are deliberate, and each one is a consequence of a frozen decision or of
the trust model.

1. **Native Windows CI has not been run.** Windows and macOS binaries
   cross-compile cleanly and path portability rules are unit-tested, but the
   Windows-to-Linux and Linux-to-Windows matrix in Gate 5 needs a native
   Windows runner to be signed off. This is the one gate item that is not fully
   evidenced.
2. **A crash during server-side materialization fails that transfer.**
   Materialization consumes staged segments, so the sender resends. Uploads
   themselves resume from the accepted offset. Rationale in
   [`docs/adr/0001-implementation-choices.md`](adr/0001-implementation-choices.md).
3. **Small-file packs are stored twice** — once as the `tar.zst` segment for
   ranged downloads, once extracted in the payload. Large files are never
   duplicated.
4. **No end-to-end encryption.** The VPS can read payloads; that is what makes
   the zero-copy local path possible (D018).
5. **Symlinks, devices, sockets, FIFOs, hardlink identity, ACLs, ownership, and
   extended attributes are not supported** (D017). Directories, regular files,
   modification times, and the executable bit are.
6. **One shared base password, no accounts** (D003, D019). Every trusted device
   holds the same secret; rotation means updating the hash and restarting.
7. **Single host.** No object storage, no clustering. The scaling boundary is
   documented in the architecture; the public contracts would survive that
   transition, but v1 does not implement it.
8. **Prometheus metrics are not implemented.** `/healthz`, `/readyz`, and
   `sss admin status` carry what operations needs.
