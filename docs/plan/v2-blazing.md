# v2: dead simple, blazing fast

The plan for protocol v2. Two promises, and every decision below serves one of
them:

> **One line, no questions.** A transfer is one command on each side. No flags,
> no modes, no tuning, no configuration file, no second prompt.
>
> **As fast as the wire.** The bottleneck is the narrowest link, the NVMe, or
> the receiver's disk — never our protocol, our CPU, or a round trip we did not
> need.

Architecture and rationale live in [ADR 0003](../adr/0003-two-modes.md). This
document is the execution plan: what ships, in what order, and the number each
step has to hit before it counts as done.

## Part 1 — the experience

### What a transfer looks like

```console
$ sss send holiday.mkv
  ⠹ direct · 412 MB/s · 8.2 GiB of 41 GiB · 1m20s left
K7M4-Q2PX                                    (copied to clipboard)
```

```console
$ sss K7M4-Q2PX
  ⠹ direct · 412 MB/s · 8.2 GiB of 41 GiB · 1m20s left
/home/you/holiday.mkv
```

That is the whole interface. Notably:

- **The bare code is the receive command.** No `recv` subcommand to remember.
  `sss <8 chars>` is unambiguous — nothing else in the grammar looks like a code.
- **The path is shown, never chosen**: `direct`, `relay`, or `local`. The user
  learns why a transfer was fast or slow without being asked to decide anything.
- **The code is on the clipboard already** (`--no-clipboard` to opt out), and
  `--qr` prints a scannable block for a phone.
- **Re-running either command resumes.** Never a prompt, never a question about
  overwriting: resume is the default and the destination is atomic regardless.

### What gets removed

| Today | v2 |
|---|---|
| `sss configure --url …` before first use | bundled in the installer; zero-config first run |
| password prompt on every invocation | one device enrollment, then a stored device token |
| `--ttl`, `--note`, `--concurrency`, `--workers` | still there, never needed; defaults are correct |
| choosing between `sssend` / `ssrecv` | one verb `send`, and the bare code to receive |

### Error vocabulary

Four things can go wrong, and each gets one actionable line, a stable exit code,
and no stack of jargon:

| Situation | Message | Exit |
|---|---|---|
| Code never existed / mistyped | `no transfer for K7M4-Q2PX — check the code` | 4 |
| Expired or deleted | `K7M4-Q2PX expired 12 minutes ago` | 4 |
| Sender vanished mid-live | `sender disconnected at 61% — nothing was written` | 6 |
| Not enrolled on this device | `run: sss enroll` | 3 |

The rule: a message names what happened, when, and the one command that fixes
it. `--json` remains exactly one object on stdout.

## Part 2 — the speed ladder

Each phase moves one of the four floors from ADR 0003: **bytes** (send only what
is missing), **crossings** (once over the wire), **latency** (no serialized
phases), **CPU** (one pass, zero copies).

### Phase 0 — harvest what is already there *(done)*

Parallel segment download, incompressible-data heuristic, HTTP/3 on the edge,
32 MiB TCP windows, 60 GiB store. No protocol change.

### Phase 1 — verified streaming *(floor: latency)*

Adopt `lukechampine.com/blake3/bao`. Store an outboard Merkle tree per segment;
the transfer's identity becomes its root hash.

- **Ships:** per-slice verification; any prefix, suffix, or hole verifies alone.
- **User-visible:** resume gets sharper — a corrupt or truncated range costs one
  chunk, not one segment.
- **Gate:** a receive that is killed and restarted at 10 random offsets produces
  a byte-identical result and re-transfers < 1% of the payload.
- **Risk:** low. Additive, no wire break, library already vendored.

### Phase 2 — cut-through follow *(floor: latency)*

A receiver attaches to a transfer that is still uploading and pulls verified
chunks immediately.

- **Ships:** wall clock drops from `upload + download` to ≈ `max(upload, download)`.
- **User-visible:** nothing. Same command, same code; it is simply faster when
  both machines happen to be up.
- **Gate:** for a 10 GiB payload with sender and receiver both present,
  end-to-end time ≤ 1.15 × the slower leg alone. Killing the sender at 60%
  leaves the destination absent and exits 6.
- **Risk:** medium — concurrency around the window. This is the phase that earns
  a TLA+ model of the state machine.

### Phase 3 — direct peer-to-peer *(floor: crossings — the big one)*

The relay becomes rendezvous plus fallback. Bytes cross the network once.

This is third, not fifth, because it is the only item that helps **every**
transfer unconditionally. Delta and dedup help a receiver who already holds
something similar; halving the crossings helps the holiday video too.

- **Ships:** the `direct` path in the progress line stops being aspirational.
  Relay disk cost for a live transfer collapses to the window.
- **Gate:** on two hosts behind ordinary NAT, ≥ 80% of transfers establish a
  direct path within 3 s; relay fallback is invisible except in the path label.
- **Risk:** high, and it is NAT-traversal risk, not protocol risk.

### Phase 4 — reclaim on receipt *(disk, not speed)*

With one receiver, the payload's useful life ends when that receiver finishes.
Return the space then; keep the TTL only as the unclaimed timeout.

- **Ships:** peak disk becomes one payload in flight instead of an accumulating
  spool — what makes large payloads viable on a small store.
- **Gate:** ten sequential 5 GiB transfers complete on a 20 GiB store with peak
  usage under 6 GiB.
- **Risk:** low, but it interacts with resume: reclamation must wait for the
  receiver's atomic rename, not merely its last byte.

### Phase 5 — PAKE end-to-end encryption *(trust, not speed)*

A PAKE over the code gives both sides a session key the relay never sees. Zero
key management: the code the user already reads aloud *is* the secret.

- **Critical:** the code carries 40 bits (32 characters, 8 of them). That is a
  sound PAKE secret — one online guess per attempt, mailbox destroyed on failure
  — and is **not** key material to be hashed. Implement SPAKE2 or equivalent;
  never `KDF(code)`.
- **Gate:** an operator with root on the relay cannot recover plaintext; the
  zero-copy local path still works for a key-holding local receiver; a wrong
  code fails in one attempt and burns the mailbox.

### Phase 6 — FastCDC chunking and pairwise delta *(floor: bytes)*

Content-defined boundaries (~64 KiB) and BLAKE3 chunk identity. The receiver
names a prior version it holds; the sender ships the difference. Protocol v2
breaks here; v1 stays serviceable until every client is enrolled.

No global content-addressed store: at 1:1 there is nothing to share between
receivers, and a CAS would buy refcounting, garbage collection, and a
chunk-existence leak for no return.

- **Gate:** resending a 10 GiB payload with a 3% edit transfers ≤ 7% of it;
  receiving a payload the machine already holds completes in < 2 s and transfers
  < 1 MiB. 10,000 small files still cost single-digit requests.
- **Risk:** high. Manifest, storage layout, and wire all change together.

### Phase 7 — transport and syscall ceiling *(floor: CPU)*

MP-QUIC over multiple interfaces, kTLS and `io_uring` for zero-copy disk→NIC.

- **Gate:** on 10 GbE, ≥ 90% of `iperf3` single-stream throughput with CPU below
  50% of one core per GB/s.
- **Note:** genuinely diminishing for two personal machines on domestic links.
  Do not start this before Phases 3 and 5 are delivering.

## Part 3 — the numbers that define success

Run before and after every phase; regressions block the merge.

| Scenario | Metric | Target |
|---|---|---|
| LAN, 10 GiB single file | throughput vs `iperf3` baseline | ≥ 90% |
| WAN, 100 ms RTT, 0.5% loss | throughput vs link capacity | ≥ 70% |
| 10,000 files, 2 GiB total | requests / throughput vs single-file | < 20 / ≥ 80% |
| Two hosts, ordinary NAT | direct path established | ≥ 80% within 3 s |
| Ten sequential 5 GiB sends | peak store usage | < 6 GiB |
| Resend, 3% edited *(Phase 6)* | wire bytes vs payload | ≤ 7% |
| Live, both present | total time vs slower leg | ≤ 1.15× |
| Cold machine → first transfer | commands / elapsed | ≤ 2 / ≤ 60 s |
| Any transfer, common path | flags the user must type | 0 |

The last row is not decoration. Every phase that adds a flag to the common path
has failed its own acceptance test, whatever it did to the throughput numbers.

## Part 4 — non-goals

Accounts, dashboards, folder sync, plugins, previews, content indexing — all
still out, per the README. Added to that list: **no user-facing mode switch**.
If v2 ever needs one, the mode-selection design in ADR 0003 was wrong.
