# Implementation Plan

Build in vertical milestones. Each milestone must leave the repository runnable and testable.

## Milestone 0 — Engineering baseline

Deliver:

- Go module and package boundaries;
- cross-platform build commands;
- deterministic clock and ID interfaces;
- structured error model;
- config loading;
- CI on Linux and Windows;
- test fixtures and black-box harness;
- `healthz` and `readyz`;
- no production endpoint stubs presented as complete.

Exit gate:

- clean build;
- unit tests;
- race test on Linux;
- cross-compile proof;
- server starts and reconciles an empty store.

## Milestone 1 — Real curl vertical slice

Deliver:

- base-password authentication;
- streaming `POST /s` for one regular file;
- staged write and digest;
- atomic publish;
- eight-character code;
- `GET /r/{code}`;
- commit-based TTL;
- janitor;
- SQLite state;
- restart reconciliation;
- plain-text error handling.

Exit gate:

```bash
CODE=$(curl -fsS -u sss -F "file=@sample.bin" "$SSS_URL/s")
curl -fsS -u sss -o received.bin "$SSS_URL/r/$CODE"
cmp sample.bin received.bin
```

The committed transfer survives a daemon restart and later expires.

## Milestone 2 — Complete simple HTTP experience

Deliver:

- repeated multipart `file` fields;
- notes;
- custom TTL;
- `/s/raw`;
- metadata endpoint;
- streaming ZIP/TAR download;
- filename and content-disposition correctness;
- idempotency-key support;
- disk admission;
- all stable errors;
- Linux and Windows curl examples.

Exit gate:

Single-file, multi-file, raw stream, note, expiry, wrong-auth, unknown-code, and insufficient-storage scenarios pass black-box tests.

## Milestone 3 — Local zero-copy path

Deliver:

- Unix socket listener;
- group-based authorization;
- local code claim;
- read-only committed payload;
- cleanup grace lease;
- plain-text and JSON local responses;
- CLI local-path auto-detection.

Exit gate:

A VPS-local process receives the existing live path with no payload copy and cleanup respects the lease.

## Milestone 4 — Excellent basic CLI

Deliver:

- `sss configure`;
- `sss send`;
- `sss recv`;
- `sss inspect`;
- `sss doctor`;
- `sss admin status`;
- `sssend` and `ssrecv` entry points;
- strict stdout/stderr behavior;
- `--json`;
- safe destination handling;
- Windows path behavior;
- optional note and TTL parsing.

The first CLI implementation may use simple endpoints for modest transfers.

Exit gate:

Documented CLI flows pass on Linux and Windows from clean installations.

## Milestone 5 — Resumable advanced transfer

Deliver:

- transfer creation API;
- upload segment resources;
- offset `HEAD`/`PATCH`;
- idempotent commit;
- client resumable state;
- raw large-file segments;
- small-file `tar.zst` packs;
- secure materializer;
- range-capable downloads;
- resumable receive;
- changed-source detection.

Exit gate:

Kill-and-resume tests pass during upload and download. A large file is not copied into a whole-transfer archive. A large small-file tree does not create one HTTP request per file.

## Milestone 6 — Production hardening

Deliver:

- fault-injection coverage;
- disk-full behavior;
- startup reconciliation matrix;
- graceful shutdown;
- concurrency limits;
- archive traversal defenses;
- file-count and size limits;
- admin cleanup/status;
- deployment files;
- upgrade/recovery runbook;
- release builds and checksums;
- performance characterization;
- documentation audit.

Exit gate:

Every gate in [`QUALITY_GATES.md`](QUALITY_GATES.md) and [`evals/release-gate.md`](evals/release-gate.md) passes with attached evidence.

## Suggested parallel waves

### Wave A

- repository/build baseline;
- public error/types contract;
- black-box test harness;
- deployment skeleton.

### Wave B

- server auth/config;
- filesystem/SQLite lifecycle;
- simple HTTP send;
- simple HTTP receive.

### Wave C

- local Unix socket;
- CLI basic UX;
- archive formats;
- restart and expiry testing.

### Wave D

- advanced resumable API;
- client segment planner;
- materializer;
- resumable receive.

### Wave E

- chaos/fault tests;
- cross-platform verification;
- packaging;
- independent review and release.

The manager owns dependency resolution and should adjust assignments based on actual team capability.
