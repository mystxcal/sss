# Storage and Lifecycle Contract

## Invariants

1. Only committed transfers have codes.
2. A committed code resolves to a complete verified payload.
3. Committed payload is immutable.
4. Final receive output appears atomically.
5. Expiry begins at commit.
6. New claims fail after expiry.
7. Active bounded leases pin data.
8. Cleanup is idempotent.
9. Staging, live, and trash share a filesystem.
10. Payload bytes are not stored in SQLite.

## Staging

Every send begins under a unique internal transfer ID.

Staging may contain partial data and is never addressable by code.

An internal staging timeout cleans abandoned sends independently of public TTL.

## Verification

Before commit:

- all declared bytes arrived;
- segment digests match;
- materialized file digests match;
- path and entry policies pass;
- limits pass;
- final manifest is durable.

## Commit

Commit assigns:

```text
committed_at = server clock
expires_at = committed_at + requested TTL
code = unique random 8-character code
```

Code allocation and committed database state occur transactionally.

## Claims

Remote claims receive bounded session authorization. Local claims receive a fixed cleanup grace.

A claim created before expiry may finish during its lease. Lease renewal requires active progress and remains bounded.

## Expiry

At expiry:

- new claim creation returns 410;
- existing lease holders may continue;
- transfer enters expired state;
- janitor schedules deletion when unpinned.

## Trash

Live content is atomically moved to trash before recursive deletion. Trash is never claimable.

## Receive finalization

Remote clients use:

```text
.<destination>.sss-partial/
```

or equivalent hidden state, verify all entries, then atomically rename to the final destination. On filesystems where directory rename atomicity differs, use the strongest native equivalent and test it.
