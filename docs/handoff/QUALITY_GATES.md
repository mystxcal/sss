# Quality Gates

A release may proceed only when every gate passes with evidence.

## Gate 1 — Contract fidelity

- All documented endpoints exist.
- Curl examples run unchanged except for URL, password, and path values.
- CLI stdout/stderr and exit-code contracts match.
- Code normalization, TTL limits, and error codes match.
- No undocumented required setup step exists.

## Gate 2 — Data correctness

- Single-file and multi-file transfers round-trip byte-for-byte.
- Notes and portable metadata round-trip.
- Digest mismatch blocks commit.
- Unsupported file types fail clearly.
- No committed code resolves to incomplete content.
- Receive output appears atomically.

## Gate 3 — Lifecycle correctness

- TTL starts at commit.
- Default and maximum TTL behavior pass.
- New claims fail after expiry.
- Active leases finish safely.
- Multiple receivers work.
- Cleanup is idempotent.
- Restart reconciliation covers every intermediate state.

## Gate 4 — Failure recovery

- Sender killed during upload.
- Server killed during upload.
- Server killed during verification/materialization.
- Server killed around publish/database commit.
- Receiver killed during download/extraction.
- Disk fills during simple upload, advanced upload, and materialization.
- Segment or manifest corruption is detected.
- Every case leaves a recoverable or cleanly failed state.

## Gate 5 — Cross-platform behavior

- Native Linux send/receive.
- Native Windows send/receive.
- Windows-to-Linux and Linux-to-Windows.
- Spaces, Unicode, long names, and separator edge cases.
- Reserved or invalid names fail deterministically.
- Aliases and shims work.

## Gate 6 — Operational readiness

- Debian 11 clean deployment works.
- Caddy/TLS path works.
- Service starts on boot.
- Unix socket permissions work.
- Logs are useful and redact secrets.
- `healthz`, `readyz`, `doctor`, and admin status work.
- Upgrade and rollback procedure is tested.
- Disk pressure is visible.

## Gate 7 — Performance and resource behavior

- Memory does not scale with full transfer size.
- Concurrency is bounded.
- Large-file throughput is limited mainly by network/disk under test conditions.
- Large small-file trees use bounded pack/request counts.
- Streaming archive download does not create a full duplicate archive.
- Local receive does not copy payload bytes.

## Gate 8 — Release integrity

- Clean repository.
- No production TODOs, panics, debug bypasses, or placeholder responses.
- Version embedded in binaries.
- Release checksums.
- Reproducible build commands.
- All evidence linked from the release report.
- Known limitations are explicit.
