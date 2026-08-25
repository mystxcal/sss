# ADR 0001: Implementation choices left open by the handoff

Status: accepted
Date: 2026-08-01

The handoff froze the product decisions (`docs/handoff/DECISIONS.md`) and
explicitly left a set of implementation choices to be decided, documented, and
benchmarked. This ADR records those choices and the few places where the
implementation had to resolve something the specification did not name.

## Libraries

| Concern | Choice | Reason |
|---|---|---|
| SQLite driver | `modernc.org/sqlite` | Pure Go; keeps CGO off (D025) so Linux and Windows cross-compile without a C toolchain |
| Digests | `lukechampine.com/blake3` | The manifest schema mandates BLAKE3; fast on the hashing-heavy send path |
| Compression | `github.com/klauspost/compress/zstd` | Streaming encoder and decoder with bounded memory for `tar.zst` packs |
| Password hashing | `golang.org/x/crypto/argon2` (argon2id) | Memory-hard hash at rest, as required by D005 |
| Config parsing | `github.com/BurntSushi/toml` | The example configuration is TOML; unknown keys are rejected rather than ignored |
| CLI | standard library `flag` | A framework would add dependency weight for a nine-command tool |

Argon2id parameters: 64 MiB, 3 iterations, 2 lanes.

## Internal thresholds

- Files at or above 1 MiB become independent raw segments.
- Smaller files are grouped into `tar.zst` packs targeting 64 MiB uncompressed.
- Four concurrent segment transfers per client.
- Server defaults: 8 concurrent uploads, 16 downloads, 4 materialization
  workers; all configurable.

These are implementation defaults, not user-facing knobs, and can be changed
without touching the protocol.

## Decisions the specification left implicit

### Successful passwords are cached as a digest in memory

Deriving a 64 MiB argon2id hash on every request would dominate the cost of an
upload. After one successful verification the SHA-256 of that password is kept
in process memory and compared in constant time on later requests. Only a digest
of an already-accepted secret is held, it never leaves the process, and it dies
with the process. Failed attempts always pay the full derivation.

### Segment identifiers are scoped to their transfer

Clients choose their own segment names (`r-0000`, `p-0000`). The `segments`
table is therefore keyed by `(transfer_id, id)`, and `upload_id` is the globally
unique handle. Without this, two concurrent sends would collide on identifier
names.

### Claim segment endpoints are not behind Basic auth

`GET /v1/claims/{id}/segments/{id}` and `POST /v1/claims/{id}/complete`
authenticate with the claim bearer token, exactly as `contracts/openapi.yaml`
declares. A single `Authorization` header cannot carry both schemes, so those
two routes are registered outside the Basic-auth chain. The claim token is
random, stored hashed, and bounded by the lease.

### Packs are retained in the live directory

A raw segment becomes the payload file itself, so large files are never
duplicated. A `tar.zst` pack cannot be: its bytes are both the wire
representation for ranged downloads and the source of many payload files. Packs
are therefore kept under `live/<shard>/<id>/packs/` alongside the extracted
payload. The cost is bounded by the small-file threshold and is what keeps a
100,000-file tree from becoming 100,000 download requests.

### A crash during materialization fails the transfer

Materialization consumes staged segments (raw segments are renamed into the
payload). A transfer interrupted mid-materialization can therefore never be
completed, so startup reconciliation marks it failed and trashes it; the sender
resends. Uploads themselves remain fully resumable — this applies only to the
window after commit begins. The alternative, copying instead of renaming, would
double the disk cost of every large file to protect against a rare crash.

### Simple uploads produce one raw segment per file

The simple `POST /s` and `POST /s/raw` paths write directly into the staged
payload while hashing. The server then synthesizes a manifest whose segments
point at those payload files. This gives curl-sent handoffs the same published
shape as CLI-sent ones, so claims, ranged downloads, and the local path work
identically regardless of how the handoff arrived.

### `--to` on a VPS-local receive copies

A local receive returns the read-only payload path. When the caller passes
`--to`, it wants writable, longer-lived files, so the payload is copied into a
hidden sibling and renamed into place atomically. Reflink is not used: it is
filesystem specific and the copy path must behave identically everywhere.

### Manifests are cached in memory

Up to 64 published manifests are held in a bounded LRU so a receive session with
many segment requests does not re-read a large manifest per request. The cache
is invalidated when a transfer is deleted.

## Deviations from the handoff

None of the frozen decisions D001–D025 were changed.
