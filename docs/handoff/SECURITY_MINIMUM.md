# Minimum Security Model

This is intentionally small. Do not turn it into an identity platform.

## Required

### Transport

- Public API only behind HTTPS.
- Daemon public listener binds to loopback by default.
- No plaintext public password traffic.

### Authentication

- HTTP Basic auth with fixed username `sss`.
- One shared base password.
- Store a memory-hard password hash, not plaintext.
- Support password rotation by updating the hash and restarting or reloading.
- Local Unix socket uses filesystem permissions.

### Secret handling

- Never log the Authorization header or password.
- Redact environment and config values in diagnostics.
- Prefer `--password-stdin`, prompt, or protected netrc for automation.
- Do not place a plaintext server password in world-readable files.

### Resource protection

- Failed-auth rate limit.
- Maximum note length.
- Maximum file count.
- Optional maximum transfer size.
- Disk high-watermark.
- Bounded concurrency.
- Staging timeout.
- Claim lease timeout.

### Filesystem safety

- Reject absolute paths.
- Reject `..` traversal.
- Reject duplicate normalized paths.
- Reject symlinks, devices, sockets, and FIFOs in v1.
- Sanitize response filenames.
- Extract packs without trusting archive headers.
- Ensure final paths stay inside the transfer root.
- Use restrictive service permissions.
- Keep committed payload read-only.

### Protocol safety

- CSPRNG IDs and codes.
- Stable errors that do not reveal passwords.
- Unknown and expired behavior must not expose internal paths.
- Idempotency keys are scoped and expire.
- Claim tokens, when used, are random and stored hashed.

## Explicitly not required for v1

- end-to-end payload encryption;
- per-device accounts or tokens;
- RBAC;
- multi-tenant isolation;
- antivirus or content scanning;
- a WAF;
- client certificates;
- OAuth/OIDC;
- an audit-log platform;
- a secret-management service.

These may be reconsidered only when the trust model changes.
