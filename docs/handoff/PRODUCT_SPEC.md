# Product Specification

## 1. Product definition

SSS is an ephemeral, authenticated, store-and-forward artifact relay for a small set of trusted devices.

A **handoff** is an immutable bundle containing:

- one or more files/directories;
- an optional note;
- portable metadata;
- a commit timestamp;
- an expiry timestamp;
- an eight-character human code.

The VPS stores the handoff until it expires and no active claim requires it.

## 2. Supported environments

### Server

- Debian 11 compatibility target.
- Single VPS.
- Local filesystem storage.
- Public HTTPS through Caddy or an equivalent reverse proxy.
- `systemd` service.

### Clients

- Linux amd64 and arm64.
- Windows amd64; arm64 should be produced when the dependency chain supports it cleanly.
- Raw HTTP clients such as `curl`.
- VPS-local clients through a Unix-domain socket.

## 3. Canonical CLI

The canonical executable is `sss`.

Convenience entry points:

- `sssend` dispatches to `sss send`;
- `ssrecv` dispatches to `sss recv`;
- `sssd` may dispatch to `sss serve`.

### Configure

```bash
sss configure --url https://drop.example.com
```

The server URL is saved. Authentication can be supplied through:

1. `SSS_PASSWORD`;
2. `--password-stdin`;
3. an interactive prompt.

Persisting the password is not required for v1. Native credential storage may be supported only if optional and cross-platform release simplicity remains intact.

### Send

```bash
sssend ./report.pdf ./results \
  --note "Review the results and return a verdict" \
  --ttl 2h
```

Successful stdout:

```text
K7M4-Q2PX
```

Progress and explanatory text go to stderr.

Supported inputs:

- regular files;
- directories;
- any mixture of both;
- one or many roots.

Rejected in v1:

- symbolic links;
- sockets;
- devices;
- FIFOs;
- paths that change materially while being read;
- duplicate virtual destination paths.

Options:

```text
--note TEXT
--note-file PATH
--ttl DURATION
--json
--quiet
--password-stdin
```

`--note` and `--note-file` are mutually exclusive.

### Receive

```bash
ssrecv K7M4-Q2PX
```

Successful stdout:

```text
/home/agent/sss-K7M4-Q2PX
```

The directory is exposed only after complete download, extraction, and verification.

Options:

```text
--to PATH
--json
--quiet
--password-stdin
```

When `--to` is absent, use a unique destination in the current directory. Never overwrite an existing path silently.

On the VPS, the client detects the local Unix socket and returns the existing immutable payload path. `--to` requests a writable copy or reflink into the selected destination.

### Inspect

```bash
sss inspect K7M4-Q2PX
```

Shows note, expiry, file count, and total bytes. `--json` returns the machine form.

### Doctor

```bash
sss doctor
```

Checks:

- configuration;
- URL reachability;
- TLS;
- authentication;
- protocol compatibility;
- local socket availability when applicable;
- server disk admission status.

## 4. Raw curl interface

### Authentication

Public endpoints use HTTP Basic authentication over HTTPS.

The username is fixed as `sss`. The password is the configured base password.

Interactive:

```bash
curl -u sss https://drop.example.com/v1/info
```

Automated:

```bash
curl -u "sss:$SSS_PASSWORD" ...
```

A protected `.netrc` or `_netrc` file is recommended for unattended raw curl use.

### Send one file

```bash
curl -fsS \
  -u sss \
  -F "file=@report.pdf" \
  https://drop.example.com/s
```

Response body:

```text
K7M4-Q2PX
```

### Send multiple files

```bash
curl -fsS \
  -u sss \
  -F "file=@report.pdf" \
  -F "file=@results.csv" \
  -F "note=Review these results." \
  -F "ttl=120" \
  https://drop.example.com/s
```

`ttl` is in minutes on the simple HTTP endpoint.

### Send a raw stream

```bash
tar -C ./project -czf - . |
  curl -fsS \
    -u sss \
    -H "X-SSS-Filename: project.tar.gz" \
    -H "X-SSS-Note: Inspect this project." \
    -H "X-SSS-TTL: 120" \
    --data-binary @- \
    https://drop.example.com/s/raw
```

### Receive

```bash
curl -fS \
  -u sss \
  -OJ \
  https://drop.example.com/r/K7M4-Q2PX
```

Auto format:

- exactly one root regular file: return the original bytes and filename;
- otherwise: stream a ZIP archive named `sss-K7M4-Q2PX.zip`.

Explicit formats:

```bash
curl -fsS -u sss \
  "https://drop.example.com/r/K7M4-Q2PX?format=tar" |
  tar -xf - -C received
```

Supported `format` values:

```text
auto
raw
zip
tar
```

`raw` is valid only for a single regular file.

### Inspect metadata

```bash
curl -fsS \
  -u sss \
  -H "Accept: application/json" \
  https://drop.example.com/v1/transfers/K7M4-Q2PX
```

### VPS-local path

```bash
curl -fsS \
  --unix-socket /run/sss/sssd.sock \
  http://localhost/local/r/K7M4-Q2PX
```

No password is needed on the protected Unix socket.

## 5. Code behavior

- Eight characters from the human-safe alphabet:

```text
0123456789ABCDEFGHJKMNPQRSTVWXYZ
```

- Displayed with a hyphen after four characters.
- Accepted with or without the hyphen.
- Case-insensitive.
- Generated with a cryptographically secure random source.
- Checked for uniqueness transactionally.
- Allocated only after the handoff has been fully verified and published.

The code is a locator. The base password is the public API credential.

## 6. Expiry

- Default: 30 minutes.
- Minimum: 1 minute.
- Maximum: 3,000 minutes.
- Countdown begins at successful commit.
- New claims are rejected after expiry.
- A claim started before expiry may finish during its lease.
- A VPS-local claim gives the payload a fixed cleanup grace period; the local path is read-only.

## 7. Immutability and multiple receivers

A committed handoff never changes.

Multiple receivers may claim the same code until expiry. A successful receive does not consume or delete the handoff.

If contents must change, create a new handoff and code.

## 8. Notes

A note is optional and bounded in size.

The note is metadata, not an injected file. It is:

- shown by `sss recv` on stderr;
- returned by metadata APIs;
- not added to the original payload;
- not required.

A real README can be included as an ordinary file.

## 9. Automation contracts

Default CLI stdout is intentionally minimal:

- send: code only;
- receive: final path only;
- inspect: human text.

Progress, notes, warnings, and diagnostics go to stderr.

With `--json`, stdout is one complete JSON object and progress is disabled unless explicitly requested.

Examples:

```json
{
  "ok": true,
  "code": "K7M4-Q2PX",
  "expires_at": "2026-08-01T20:40:00+03:00"
}
```

```json
{
  "ok": true,
  "code": "K7M4-Q2PX",
  "path": "/work/sss-K7M4-Q2PX",
  "local_zero_copy": false
}
```

## 10. Failure behavior

- No code is returned for an incomplete transfer.
- No final receive path is exposed for incomplete or unverified output.
- Rerunning an advanced CLI send or receive resumes when safe.
- A source file changed during upload causes a clear failure.
- Disk pressure rejects or aborts safely without publishing incomplete state.
- Expired codes produce a stable error and HTTP 410.
- Unknown codes produce HTTP 404.
- Authentication failures produce HTTP 401.
- Server logs include a request ID but never the password.

## 11. Non-functional requirements

- Streaming memory usage; never buffer a full transfer in RAM.
- Bounded concurrency.
- Graceful shutdown.
- Crash reconciliation on startup.
- Cross-platform path normalization.
- Atomic publication and receive finalization.
- Deterministic, documented error codes.
- No external database, queue, or object store.
