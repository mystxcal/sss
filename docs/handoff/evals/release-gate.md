# Release Gate

The manager must answer every item with **PASS plus evidence** or block the release.

## User flows

- [ ] Raw curl single-file send and receive
- [ ] Raw curl multi-file send and receive
- [ ] Raw stream send
- [ ] Metadata inspect
- [ ] CLI send files and directories
- [ ] CLI receive remote
- [ ] CLI or Unix-socket receive local path
- [ ] JSON automation
- [ ] Default 30-minute TTL
- [ ] Custom and maximum TTL

## Invariants

- [ ] Code only after commit
- [ ] Immutable committed payload
- [ ] No partial receive output
- [ ] Multiple receivers
- [ ] Commit-based expiry
- [ ] Active claim lease
- [ ] Atomic live and trash transitions
- [ ] Startup reconciliation
- [ ] Bounded memory and concurrency

## Failure proof

- [ ] Upload interruption
- [ ] Server interruption at commit fault points
- [ ] Receive interruption
- [ ] Disk full
- [ ] Corrupt segment or manifest
- [ ] Cleanup and claim race
- [ ] Changed source
- [ ] Destination conflict

## Platforms

- [ ] Debian 11 server
- [ ] Linux amd64 client
- [ ] Linux arm64 build
- [ ] Native Windows client
- [ ] Windows and Linux transfer matrix

## Operations

- [ ] Caddy deployment
- [ ] systemd boot and restart
- [ ] health and readiness
- [ ] doctor and admin status
- [ ] secret-redaction audit
- [ ] upgrade and recovery
- [ ] checksums and version metadata

## Integrity of the claim

- [ ] No required test skipped
- [ ] No placeholder production behavior
- [ ] No docs-only feature claim
- [ ] Raw evidence retained
- [ ] Known limitations disclosed
