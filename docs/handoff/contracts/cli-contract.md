# CLI Contract

## Commands

```text
sss configure
sss send
sss recv
sss inspect
sss doctor
sss admin status
sss admin cleanup
sss serve
sss hash-password
sss version
```

Aliases:

```text
sssend -> sss send
ssrecv -> sss recv
sssd   -> sss serve (optional)
```

## Authentication precedence

1. explicit `--password-stdin`;
2. `SSS_PASSWORD`;
3. interactive prompt when stdin is a terminal;
4. fail with `AUTH_REQUIRED`.

Do not accept a normal `--password VALUE` flag because it predictably exposes secrets in shell history and process arguments.

## Server URL precedence

1. explicit `--url`;
2. `SSS_URL`;
3. saved client configuration;
4. fail with configuration error.

## Duration parsing

Accept:

```text
30m
2h
3000m
```

Normalize to whole minutes. Enforce 1 through 3000.

## Default stdout

### Send

Exactly:

```text
K7M4-Q2PX\n
```

### Receive

Exactly:

```text
<absolute-or-useful-final-path>\n
```

### Local receive

The existing live payload path.

Progress, note, expiry, and warnings use stderr.

## JSON mode

One JSON object to stdout. No progress bar.

Success:

```json
{
  "ok": true,
  "operation": "send",
  "code": "K7M4-Q2PX",
  "expires_at": "2026-08-01T20:40:00+03:00"
}
```

Failure:

```json
{
  "ok": false,
  "error": {
    "code": "TRANSFER_EXPIRED",
    "message": "transfer has expired",
    "request_id": "..."
  }
}
```

## Exit codes

| Exit | Meaning |
|---:|---|
| 0 | success |
| 2 | invalid CLI usage or config |
| 3 | authentication |
| 4 | transfer not found or expired |
| 5 | network or protocol |
| 6 | integrity or state conflict |
| 7 | local filesystem or resource |
| 8 | server or internal failure |

The stable error code is more precise than the process exit code.

## Destination rules

- Default remote receive destination: unique `sss-<CODE>` path in current directory.
- Work inside a hidden partial sibling.
- Finalize with atomic rename.
- Never overwrite by default.
- `--to` must fail when unsafe.
- VPS-local receive returns the read-only existing path unless `--to` is used.

## Progress

Use human progress only when stderr is a TTY. Non-TTY stderr receives concise phase messages unless `--quiet`.

Phases:

```text
Scanning
Packing
Uploading
Committing
Claiming
Downloading
Extracting
Verifying
Finalizing
```

## Local detection

If the configured or default Unix socket exists and responds with a compatible protocol, prefer it. Do not infer locality from DNS or hostname.
