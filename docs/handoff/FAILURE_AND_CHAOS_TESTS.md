# Failure and Chaos Test Matrix

The implementation should expose deterministic test-only fault points through dependency injection or a non-production build tag. Do not ship an unauthenticated runtime fault endpoint.

## Commit fault points

Inject process termination:

1. after staging directory creation;
2. after first payload write;
3. after all segment writes;
4. after digest verification;
5. after materialization;
6. after final manifest write;
7. after manifest fsync;
8. after read-only permissions;
9. immediately before staging-to-live rename;
10. immediately after rename;
11. during SQLite commit before code allocation;
12. after code allocation before response;
13. after response write begins.

For each point:

- restart;
- run reconciliation;
- inspect database and filesystem;
- attempt receive when a code exists;
- verify no code resolves to incomplete data;
- verify unreachable garbage is eventually reclaimed.

## Delete fault points

Terminate:

- before state changes to deleting;
- after state change;
- before live-to-trash rename;
- after rename;
- during recursive delete;
- before database tombstone completion.

Deletion must be safe to repeat.

## Upload faults

- connection reset;
- client process kill;
- server process kill;
- offset mismatch;
- repeated PATCH;
- corrupted segment;
- stale idempotency key;
- source file changed;
- disk full;
- permission failure;
- file removed during read.

## Receive faults

- connection reset;
- range request repeated;
- local disk full;
- destination conflict;
- corrupted cached segment;
- extraction failure;
- receiver killed before final rename;
- code expires after claim;
- claim lease expires during inactivity.

## Concurrency faults

- two commits for the same transfer;
- many code allocations;
- expiry racing claim;
- janitor racing download;
- local claim racing deletion;
- two receivers choosing the same local destination;
- admin cleanup during active upload.

## Required evidence

For every scenario capture:

- test case ID;
- binary version and commit;
- fault point;
- commands;
- expected invariant;
- observed result;
- database state;
- relevant directory tree;
- logs;
- hashes when data exists.

A green unit test alone is not enough for crash claims. Use subprocess termination and real files.
