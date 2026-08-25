# ADR 0003: Two delivery modes over one verified, content-addressed substrate

Status: proposed
Date: 2026-08-21
Supersedes: [ADR 0002](0002-live-handoff.md)

ADR 0002 proposed live handoff and paid for it by weakening invariant 1: a code
would resolve to a payload that was not yet complete. That trade is unnecessary.
BLAKE3 is a Merkle tree, and `lukechampine.com/blake3/bao` — already in this
project's dependency tree — verifies an arbitrary slice against a root hash
without the rest of the payload existing. Per-byte verification therefore does
not require whole-payload verification. Invariant 1 gets *stronger*, not weaker,
and live delivery falls out as a consequence rather than an exception.

That reframes the product. A transfer stops being "a payload the server stores"
and becomes **a root hash the receiver resolves from wherever it can**. Two
delivery modes then serve two genuinely different situations, and they share one
mechanism.

## The substrate both modes stand on

1. **Content-defined chunking.** FastCDC boundaries (~64 KiB average) instead of
   fixed offsets, so an edit shifts one chunk rather than every subsequent one.
2. **Content addressing.** Chunk identity is its BLAKE3 hash; the transfer's
   identity is the Bao root hash of the payload. The eight-character code is a
   human-carryable pointer to that root, and nothing more.
3. **Verified streaming.** Every chunk carries its Merkle path. A receiver
   validates chunk *n* against the root with no knowledge of chunk *n-1*, so
   bytes may arrive out of order, in parallel, from several sources, none of
   which need be trusted.
4. **Pairwise delta.** The receiver names a prior version it already holds; the
   sender ships only the difference. The wire cost of a transfer becomes the
   receiver's *delta*, not the payload's size.
5. **End-to-end encryption, keyed by the code.** With exactly two parties, a
   PAKE over the code gives both sides a session key the relay never sees — no
   key management at all. The relay handles ciphertext it cannot read and cannot
   corrupt undetectably, because every chunk is verified against the root.

Point 3 is load-bearing: it is what makes live delivery correct rather than a
compromise.

### Scope: one sender, one receiver

This design assumes **1:1 transfers over the internet, asynchronous or live**.
That assumption is doing real work, and three things follow from it that would
be wrong for a one-to-many product:

- **No global content-addressed store.** A CAS earns its refcounting, garbage
  collection, and chunk-existence information leak by sharing chunks across
  receivers. With one receiver there is nothing to share. Pairwise delta against
  a named prior version gets most of the benefit with none of the machinery.
- **No fountain coding.** RaptorQ's value is reconstructing from an uncoordinated
  mix of sources. One sender and one receiver are not a mix; QUIC's own loss
  recovery suffices.
- **No reuse window.** See Mode B: a payload is reclaimed when its receiver
  finishes, not when a timer expires.

If one-to-many ever becomes a goal, revisit all three — they are the right
answers to a different question.

## Mode A — live

For when both machines are present at the same time.

- The relay is a **rendezvous and a fallback path**, not the transport. Direct
  peer-to-peer is attempted first; bytes then cross the network once, which is
  the floor.
- Relay-side storage is a **bounded window**, not the payload. Bytes are
  forwarded and dropped. Disk cost is `O(window)`, independent of payload size —
  a 70 GiB transfer through a 1 GiB window.
- Backpressure is real: the sender advances at the rate of the slowest attached
  receiver, with a configurable spill threshold before it stalls.
- Resume is possible only inside the window. Past it, the transfer degrades to
  Mode B or fails loudly — never silently.

Wall clock approaches `max(upload, download)` and, on the direct path, simply
`bytes / capacity`.

## Mode B — deferred

For the case the product exists to serve: the two machines are never up
together. Today's mode, taken to its peak.

- The payload is fully resident, immutable, and sealed. Disk cost is
  `O(payload)`. This is not a flaw; it is the price of asynchrony, and it is
  information-theoretically unavoidable.
- **Reclaimed on receipt, not on expiry.** The single receiver finishing is the
  end of the payload's useful life, so the space is returned then. Peak disk
  becomes one payload in flight rather than an accumulating spool — which is
  what makes large payloads viable on a small store. The TTL survives only as
  the *unclaimed* timeout: how long we wait for a receiver who may never come.
- Resume is arbitrary and unbounded: any prefix, any suffix, any hole, verified
  per slice.
- The zero-copy local path survives untouched — a receiver on the relay itself
  still gets a path, not a copy.
- Cut-through follow, the whole point of ADR 0002, is now free: a receiver may
  begin pulling verified chunks before the sender commits, because verification
  never depended on commit.

## How a transfer picks its mode

Not with a flag. The sender cannot know whether a receiver will appear, so
asking it to declare one is asking it to guess.

A transfer opens with a code and begins accepting bytes. If a receiver is
attached, chunks are forwarded through the window as they are verified — Mode A.
If none is attached, verified chunks land and stay — Mode B. A live transfer
whose receiver disappears **degrades to deferred** by spilling its window rather
than failing. A deferred transfer that a receiver joins mid-upload **promotes to
live** for the remainder.

The two modes are therefore two *guarantees a receiver can rely on*, not two
code paths a sender selects. An explicit `--live` (refuse to spill; fail if no
receiver attaches) remains available for the case where a sender genuinely wants
to bound the relay's disk use.

## Amended invariants

Invariant 1 becomes stronger and simpler:

> 1. Every byte a receiver uses is verified against the transfer's root hash
>    before use, whatever its source and whatever order it arrives in. A
>    receiver's output is complete or absent, never partial.

Invariant 4 becomes:

> 4. The expiry clock starts when a payload becomes durable, not at upload
>    start. A live transfer holds no expiry clock; its window is bounded by
>    configuration, and expiry begins if and when it spills to deferred.

Invariants 2, 3, 5, 6, 7, and 8 are unchanged. Invariant 3 — atomic receive
output — remains the reason an interrupted live follow is harmless.

## What we accept

- **Two failure vocabularies.** A receiver must be able to distinguish "this
  code is not deliverable right now" from "this code is gone," and the CLI must
  say which. This is the main new surface area for user confusion.
- **Backpressure is user-visible** in Mode A. A slow receiver throttles a
  sender, which never happens today.
- **Delta negotiation leaks a little information** to the relay: naming a prior
  version proves possession of it. End-to-end encryption bounds this without
  erasing it.
- **A rewrite, not a patch.** Chunking changes the manifest, the storage layout,
  and the wire protocol. It is protocol version 2.
- **A 40-bit code is a PAKE secret, not key material.** The alphabet is 32
  characters and the code is 8, so it carries 40 bits. That is sufficient for a
  PAKE, which permits one online guess per attempt and destroys the mailbox on
  failure. It is *not* sufficient to hash into a key. Confusing the two is the
  classic way this design fails.

## Rejected

- **Weakening invariant 1** (ADR 0002's approach). Verified streaming makes the
  compromise unnecessary; a design that needed it was a design lacking a tool.
- **A custom UDP transport.** FASP-style protocols earned their advantage
  against TCP Reno on long fat pipes; BBR and QUIC have since closed most of it.
  Use MP-QUIC and spend the effort on chunking instead.
- **Pass-through as a third mode.** It is Mode A's storage behaviour, not a
  separate contract, and naming it separately would expose an implementation
  detail as a user-facing choice.

## Implementation order

Each step is independently useful, and none requires the next:

Ordered for 1:1. The unconditional wins come before the conditional ones:
direct delivery helps *every* transfer, while delta only helps a receiver who
already holds something similar.

1. **Bao verified streaming** over today's segments. Smallest change, already
   vendored, and it retro-fixes resume: any prefix verifies.
2. **Cut-through follow** on top of it — ADR 0002's benefit, now with no
   invariant cost.
3. **Direct peer-to-peer** with the relay as rendezvous. The crossings floor,
   and the largest unconditional win in this list.
4. **Reclaim on receipt**, collapsing peak disk to one payload in flight.
5. **PAKE end-to-end encryption**, closing the "the server can read your
   payload" concession in the README.
6. **FastCDC chunking and pairwise delta.** The protocol v2 break; pays only
   when the receiver holds a prior version, which is why it is no longer third.
7. MP-QUIC, kTLS and `io_uring` zero-copy, as link speeds justify them.
