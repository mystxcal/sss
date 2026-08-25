# Acceptance Tests

These are behavioral tests. Implementations may differ internally.

## A. Authentication

### A1. Missing password

```bash
curl -sS -o body -w '%{http_code}' "$SSS_URL/v1/info"
```

Expected:

- HTTP 401;
- stable `AUTH_REQUIRED` error;
- `WWW-Authenticate` header;
- no transfer state created.

### A2. Wrong password

Expected:

- HTTP 401;
- stable `AUTH_INVALID`;
- rate-limit accounting;
- no secret in logs.

## B. Simple send and receive

### B1. One file

```bash
printf 'alpha\n' > alpha.txt
CODE=$(curl -fsS -u "sss:$SSS_PASSWORD" \
  -F "file=@alpha.txt" "$SSS_URL/s")
curl -fsS -u "sss:$SSS_PASSWORD" \
  -o alpha.out "$SSS_URL/r/$CODE"
cmp alpha.txt alpha.out
```

Expected:

- code matches the documented alphabet and `XXXX-XXXX` form;
- bytes match;
- metadata reports one root;
- code remains usable for another receiver.

### B2. Multiple files

Upload two files with repeated `file` fields.

Expected:

- auto receive returns ZIP;
- both names and bytes match;
- no synthetic note file is added.

### B3. Raw stream

Pipe an archive to `/s/raw` with `X-SSS-Filename`.

Expected:

- returned transfer contains exactly that named file;
- bytes match the stream.

### B4. Note and TTL

Send with note and `ttl=1`.

Expected:

- metadata returns exact note;
- expiry is based on commit time;
- new receive works before expiry;
- new receive returns 410 after expiry.

### B5. Maximum TTL

- `ttl=3000` succeeds.
- `ttl=3001` returns `TTL_OUT_OF_RANGE`.
- omitted TTL uses 30.

## C. Code normalization

For one committed code, all valid variants resolve:

```text
K7M4-Q2PX
k7m4-q2px
K7M4Q2PX
k7m4q2px
```

Invalid characters or wrong length return `INVALID_CODE`.

## D. Publication and visibility

### D1. Interrupted upload

Terminate client mid-request.

Expected:

- no code;
- no committed record;
- staging data eventually cleaned;
- no receive path can expose it.

### D2. Partial receive

Terminate receiver mid-download or extraction.

Expected:

- only hidden partial state exists;
- final destination does not;
- rerun resumes or restarts safely;
- final rename occurs only after verification.

## E. Multiple receivers

Claim and download the same code concurrently from two clients.

Expected:

- both succeed;
- bytes and manifests match;
- one completion does not delete the transfer.

## F. VPS-local behavior

```bash
PATH_OUT=$(curl -fsS \
  --unix-socket /run/sss/sssd.sock \
  "http://localhost/local/r/$CODE")
```

Expected:

- path is inside configured live root;
- path already exists;
- content matches;
- no payload copy is created;
- payload is not writable by ordinary local agent;
- cleanup waits for local grace.

## G. Restart

Restart the daemon after commit.

Expected:

- code remains resolvable;
- expiry remains unchanged;
- manifest remains valid.

Restart at each injected commit boundary.

Expected:

- reconciliation reaches a valid terminal state;
- no incomplete code is published.

## H. Disk pressure

Force available space below admission threshold.

Expected:

- new sends return 507 / `INSUFFICIENT_STORAGE`;
- existing committed downloads remain available;
- no database/filesystem reservation leak after recovery.

## I. Advanced resumability

- Interrupt each segment upload at several offsets.
- Restart client and server.
- Rerun the same `sssend`.
- Verify accepted offsets and final bytes.
- Modify a source before rerun; verify clear refusal.
- Interrupt receive; verify range resume and atomic final directory.

## J. Cross-platform

Run native tests for:

- Linux sender to Windows receiver;
- Windows sender to Linux receiver;
- spaces;
- Unicode;
- executable bit from Linux;
- Windows-invalid destination names;
- deep but supported paths.

## K. Automation contract

```bash
CODE=$(sssend alpha.txt 2>progress.log)
```

Expected:

- `$CODE` contains only normalized code plus newline;
- progress and log output are on stderr.

```bash
sssend alpha.txt --json | jq -e '.ok and .code'
```

Expected one JSON object and no progress contamination.
