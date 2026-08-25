# ADR 0002: Live handoff — codes that resolve while the payload is still arriving

Status: superseded by [ADR 0003](0003-two-modes.md)
Date: 2026-08-21

> Superseded on the day it was written. This ADR bought live delivery by
> weakening invariant 1. BLAKE3 verified streaming (`bao`, already vendored)
> makes that payment unnecessary: a slice verifies against the root hash without
> the rest of the payload. Kept for the record, and because its failure analysis
> and rejected alternatives still hold.

Store-and-forward serializes the two halves of a transfer: the receiver cannot
start until the sender has finished. Wall clock is `upload + download`, never
`max(upload, download)`, and for a large payload over a slow uplink that
doubling is the dominant cost in the product — larger than any transport tuning
available to us.

This ADR removes that serialization. It is not a new mode, a new command, or a
new flag. A receiver that asks for a code whose upload is still in progress
follows it; a receiver that asks for a finished code gets today's path. The
sender never chooses, and the receiver never has to know which case it is in.

## What changes

`AGENTS.md` invariant 1 read:

> Only committed transfers have codes, and a code always resolves to complete,
> verified content.

It conflated two promises: **when a code is allocated** and **whether delivered
bytes can be trusted**. Only the second is load-bearing. Amended:

> 1. A code resolves only to verified bytes, and a receiver's output is complete
>    or absent — never partial. A code may be allocated when a transfer opens,
>    before its payload has fully arrived; a transfer is therefore `live` or
>    `committed`, and both states are addressable.

Invariant 4 read:

> TTL starts at commit, not at upload start.

Amended, preserving the intent that a slow upload must not eat the recipient's
window:

> 4. The expiry clock starts at commit, not at upload start. A live transfer has
>    no expiry clock; it is bounded instead by the staging deadline
>    (`storage.staging_ttl_minutes`), and its TTL begins when it commits.

Invariants 2, 3, 5, 6, 7, and 8 are unchanged, and this design depends on 3 in
particular: atomic receive output is precisely what makes an interrupted follow
harmless. A follower cut off at 90% leaves the destination untouched, because
the bytes were landing in a `.sss-partial` sibling that is discarded.

## How it works

Segments are already immutable, independently digested, and independently
resumable. That is the whole mechanism — a segment verified at the server is
publishable whether or not its siblings have arrived.

- A code is allocated when the transfer opens, in the same database transaction
  that creates the staging directory. Codes remain unguessable and are still
  never reused.
- `GET /r/<code>` on a live transfer streams every verified segment, then
  long-polls for the next one until the transfer commits or fails. The receiver
  verifies each segment on arrival exactly as it does today, and materializes
  into `.sss-partial` exactly as it does today.
- A sender abort fails the transfer; in-flight followers receive
  `ErrTransferFailed` (exit 6) mid-stream, discard the partial directory, and
  print nothing to stdout. Re-running the code then reports the failure rather
  than hanging.
- Multiple followers are independent readers of the same immutable segments; no
  reader affects another, and none blocks the sender.
- `inspect` gains a `state` field (`live` or `committed`) and reports the byte
  count verified so far. This is the only user-visible protocol addition.

## What we accept

- **A code no longer implies the payload exists in full.** This is the real
  cost. It is why `inspect` must report state, and why the follow path must fail
  loudly rather than stall silently when a sender disappears.
- **Followers cannot resume a dead transfer.** Once the sender is gone, the
  segments that landed are useless; the transfer fails and is trashed. Resume
  remains available only while the sender lives.
- **Admission accounting gets harder.** Reservations already exist for in-flight
  uploads; followers add read load but no new disk, so the watermark logic is
  unchanged.

## Alternatives rejected

- **A separate `--live` flag.** Forces the sender to predict whether a receiver
  will show up early, which is exactly the thing the sender cannot know. Two
  code paths, two sets of semantics, for no gain.
- **Peer-to-peer transport.** It would beat this on wall clock by crossing the
  wire once instead of twice, and it is ruled out by product scope: the relay
  exists so the two machines never have to be online together.
