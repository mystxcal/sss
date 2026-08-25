# Working in this repository

SSS is an ephemeral store-and-forward artifact relay. The complete product
intent, frozen decisions, and contracts live under
[`docs/handoff/`](docs/handoff/) and are **not** to be edited: they are the
original specification. Record any justified deviation as an ADR in
[`docs/adr/`](docs/adr/).

Authority order when documents disagree:

1. `docs/handoff/OWNER_INTENT.md`
2. `docs/handoff/DECISIONS.md`
3. `docs/handoff/contracts/`
4. `docs/handoff/PRODUCT_SPEC.md`
5. `docs/handoff/ARCHITECTURE.md`

## Invariants that must never regress

1. Every byte a receiver uses is verified against the transfer's root hash
   before use, whatever its source and whatever order it arrives in. A
   receiver's output is complete or absent, never partial. (Amended by
   [ADR 0003](docs/adr/0003-two-modes.md); until protocol v2 ships, the
   implementation satisfies this the stricter way — only committed transfers
   have codes.)
2. A committed payload is immutable and read-only.
3. Receive output appears atomically; a receiver never sees partial output.
4. The expiry clock starts when a payload becomes durable, not at upload start.
   A live transfer holds no expiry clock; expiry begins if and when it spills to
   deferred.
5. Payload bytes never live in SQLite; `staging`, `live`, and `trash` share one
   filesystem so publication and deletion are renames.
6. Default CLI stdout is machine-minimal: `send` prints only the code, `recv`
   only the final path. Everything else is stderr.
7. Passwords and `Authorization` headers are never logged.
8. Manifest and archive input is treated as hostile: no traversal, no duplicate
   normalized paths, no unsupported entry types.

If a change makes one of these harder to guarantee, it is the wrong change.

## Package boundaries

```text
cmd/sss            entry point and alias dispatch
internal/app       composition: listeners, reconciliation, shutdown
internal/api/*     HTTP handlers and middleware; no state transitions here
internal/transfer  lifecycle service; owns every invariant above
internal/store/*   sqlite metadata and staging/live/trash operations
internal/materialize  pack extraction and verification (treats input as hostile)
internal/client    the only implementation of the client side of the protocol
internal/cli       user interaction only
internal/protocol  pure models, codes, errors, validation (no I/O)
internal/platform  the only place with OS-specific behavior
```

Handlers call the transfer service; they never implement lifecycle rules
themselves. The CLI calls the client package; it never contains a second
transfer protocol. `protocol` must not import storage or rendering.

Avoid packages named `utils`, `common`, or `helpers`.

## Before you push

```bash
make check     # go vet + the full suite
make race      # concurrency-sensitive changes
```

Add tests at the layer the behavior lives at:

- pure rules → package unit tests;
- repository correctness → real SQLite and temp filesystems;
- user-visible behavior → `integration/blackbox` through HTTP, the socket, or
  the CLI;
- crash behavior → `integration/faults`.

A test that only restates the implementation is not worth writing. Test what a
user or an agent would notice.

## Style

Match the surrounding code: standard library first, small focused packages,
errors as stable `protocol.Error` values with documented codes. Comments explain
why something is the way it is, not what the next line does.

Do not add dependencies casually. The build is CGO-free on purpose so that Linux
and Windows binaries cross-compile without a C toolchain; anything requiring CGO
needs an ADR and a strong reason.
