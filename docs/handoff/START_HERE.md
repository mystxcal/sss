# Start Here

## Mission

Build and ship **SSS**: a single-VPS, store-and-forward artifact relay that lets agents on Windows, Linux, and the VPS exchange files with almost no ceremony.

The core experience must be:

```bash
sssend ./report.pdf ./results --note "Review this" --ttl 2h
# K7M4-Q2PX

ssrecv K7M4-Q2PX
# /home/agent/sss-K7M4-Q2PX
```

The same system must work without installing the CLI:

```bash
curl -fsS -u sss -F "file=@report.pdf" https://drop.example.com/s
# K7M4-Q2PX

curl -fS -u sss -OJ https://drop.example.com/r/K7M4-Q2PX
```

On the VPS, receipt must return the already-materialized path instead of downloading or duplicating the files:

```bash
curl -fsS \
  --unix-socket /run/sss/sssd.sock \
  http://localhost/local/r/K7M4-Q2PX
# /srv/sss/live/7c/<transfer-id>/payload
```

## Product doctrine

1. **The HTTP interface is the product.** The CLI is a thin, excellent client.
2. **One shared base password is enough for v1.** Do not invent accounts, organizations, roles, or enrollment.
3. **A handoff is immutable and asynchronous.** Sender and receiver never need to be online together.
4. **A short code is a locator, not the sole credential.** Authentication is separate.
5. **Incomplete data is never published.** The code is allocated only after successful commit.
6. **The receiver never sees partial output.**
7. **VPS-local receipt returns a path, not another copy.**
8. **Reliability complexity is allowed only where it directly removes user pain.**
9. **Do not turn this into sync software, object-storage infrastructure, or a platform.**
10. **Perfection means removing everything that does not materially improve this job.**

## Authority order

When documents appear to conflict, resolve them in this order:

1. [`OWNER_INTENT.md`](OWNER_INTENT.md)
2. [`DECISIONS.md`](DECISIONS.md)
3. Explicit contracts under [`contracts/`](contracts/)
4. [`PRODUCT_SPEC.md`](PRODUCT_SPEC.md)
5. [`ARCHITECTURE.md`](ARCHITECTURE.md)
6. Implementation planning material

Do not silently reinterpret a frozen decision. Record any justified deviation in the implementation repository as an ADR and explain the evidence.

## Required reading order

1. [`MANAGER_AGENT_MANDATE.md`](MANAGER_AGENT_MANDATE.md)
2. [`OWNER_INTENT.md`](OWNER_INTENT.md)
3. [`DECISIONS.md`](DECISIONS.md)
4. [`PRODUCT_SPEC.md`](PRODUCT_SPEC.md)
5. [`ARCHITECTURE.md`](ARCHITECTURE.md)
6. [`contracts/`](contracts/)
7. [`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md)
8. [`TASK_GRAPH.yaml`](TASK_GRAPH.yaml)
9. [`QUALITY_GATES.md`](QUALITY_GATES.md)
10. [`ACCEPTANCE_TESTS.md`](ACCEPTANCE_TESTS.md)

## Manager's first actions

1. Create or inspect the implementation repository.
2. Install this package under `docs/handoff/` without editing the original copies.
3. Establish a clean Go build, test, lint, and cross-compile baseline.
4. Convert [`TASK_GRAPH.yaml`](TASK_GRAPH.yaml) into owned work orders.
5. Assign the first parallel wave:
   - protocol and shared types;
   - server storage/state machine;
   - black-box test harness;
   - deployment/release skeleton.
6. Build the first real vertical slice before broadening the system:
   - authenticated `POST /s`;
   - atomic commit;
   - eight-character code;
   - authenticated `GET /r/{code}`;
   - TTL cleanup;
   - one-file round trip through real `curl`.
7. Preserve evidence after every milestone.

## What counts as completion

Completion is a release, not a repository that looks busy.

The final delivery must include:

- a production server binary;
- Windows and Linux client binaries;
- `sssend` and `ssrecv` aliases or shims;
- documented raw `curl` operation;
- Debian 11-compatible deployment assets;
- a functional local Unix-socket receive path;
- resumable advanced transfers;
- deterministic lifecycle and cleanup;
- cross-platform and fault-injection test evidence;
- reproducible build instructions;
- a release manifest and checksums;
- no placeholder production paths, fake integrations, or tests that merely restate implementation details.

See [`QUALITY_GATES.md`](QUALITY_GATES.md) and [`evals/release-gate.md`](evals/release-gate.md).
