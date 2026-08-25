# Frozen Decisions

These decisions are binding for the first production release unless executable evidence shows that one makes the product materially worse.

| ID | Decision | Rationale |
|---|---|---|
| D001 | Use a single VPS store-and-forward architecture. | Sender and receiver need not be online together. |
| D002 | The HTTP API is first-class; the CLI is a client, not the only interface. | Raw `curl` must work with no installation. |
| D003 | Use one shared base password for all public operations in v1. | Matches the trusted-device use case and minimizes setup. |
| D004 | Use HTTPS Basic authentication with fixed username `sss`. | Universally supported by curl and simple clients. |
| D005 | Store only a password hash; never plaintext. | Minimal sensible protection at negligible product cost. |
| D006 | Use an eight-character, case-insensitive, human-safe Base32 code displayed `XXXX-XXXX`. | Easy agent/human transcription; authentication remains separate. |
| D007 | Allocate the code only after verified commit. | No recipient receives a code for incomplete content. |
| D008 | TTL begins at commit. Default 30 minutes; maximum 3,000 minutes. | Upload time must not consume useful availability. |
| D009 | Committed handoffs are immutable and reusable by multiple receivers until expiry. | Clean semantics, safe retries, multi-agent use. |
| D010 | Use one Go codebase and one primary binary. | Cross-platform release and operational simplicity. |
| D011 | Use SQLite for metadata and the filesystem for bytes. | One-host workload does not justify a database service. |
| D012 | `staging`, `live`, and `trash` share one filesystem. | Atomic directory rename is central to correctness. |
| D013 | VPS-local receipt uses a protected Unix socket and returns the existing path. | Avoids network transfer and duplicate storage. |
| D014 | Simple curl uploads are streaming but non-resumable. | Preserve universal simplicity. |
| D015 | The CLI advanced path supports resumable segmented transfer. | Removes pain for large or unreliable transfers. |
| D016 | Use raw segments for large files and bounded `tar.zst` packs for small files. | Handles both giant files and huge file trees efficiently. |
| D017 | Reject symlinks and non-regular filesystem objects in v1. | Avoid ambiguous and unsafe cross-platform semantics. |
| D018 | No end-to-end encryption in v1. | Conflicts with immediate materialized VPS paths and is not required for the trusted VPS model. |
| D019 | No accounts, RBAC, dashboard, P2P, sync, object store, queue, or external database. | They do not improve the core job enough to justify themselves. |
| D020 | Default CLI stdout is machine-minimal; progress and notes use stderr. | Makes shell and agent automation trivial. |
| D021 | Receivers never see partial final output. | Atomic destination rename is a user-facing correctness guarantee. |
| D022 | Active claims may finish after code expiry under a bounded lease. | Expiry should stop new claims, not destroy in-flight work. |
| D023 | Use stable, documented error codes across CLI and HTTP. | Agents need deterministic recovery behavior. |
| D024 | Debian 11 is a server compatibility target. | Matches the owner's VPS. |
| D025 | Release binaries should not require CGO unless compelling evidence requires it. | Simplifies Linux/Windows cross-compilation and deployment. |

## Decisions intentionally left to implementation

The manager may decide, document, and benchmark:

- exact maintained Go libraries;
- exact internal pack thresholds;
- exact bounded concurrency defaults;
- exact SQLite schema details;
- whether to embed a tus implementation or implement the required compatible subset;
- whether a local `--to` operation uses reflink or ordinary copy;
- final service hardening flags compatible with Debian 11;
- packaging format beyond standalone binaries and service assets.

These choices must not alter public behavior.
