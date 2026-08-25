# Implementation Checklist

## Foundation

- [ ] Go module
- [ ] configuration
- [ ] protocol versions
- [ ] error codes
- [ ] deterministic clock and IDs
- [ ] SQLite migrations
- [ ] filesystem repository
- [ ] structured logging
- [ ] CI and cross-build

## Simple product

- [ ] Basic auth
- [ ] `/s`
- [ ] `/s/raw`
- [ ] `/r/{code}`
- [ ] metadata
- [ ] notes
- [ ] TTL
- [ ] code generation
- [ ] atomic commit
- [ ] streaming ZIP and TAR
- [ ] idempotency
- [ ] disk admission
- [ ] expiry cleanup

## VPS local

- [ ] Unix socket
- [ ] group permissions
- [ ] local path claim
- [ ] read-only payload
- [ ] cleanup grace
- [ ] no-copy evidence

## CLI

- [ ] configure
- [ ] send
- [ ] recv
- [ ] inspect
- [ ] doctor
- [ ] admin status and cleanup
- [ ] hash-password
- [ ] aliases and shims
- [ ] JSON mode
- [ ] stdout and stderr contract
- [ ] Linux native
- [ ] Windows native

## Advanced transfer

- [ ] create transfer
- [ ] offset HEAD and PATCH
- [ ] persisted resume
- [ ] raw segments
- [ ] tar.zst packs
- [ ] materializer
- [ ] digest verification
- [ ] claim tokens and leases
- [ ] range downloads
- [ ] atomic receive
- [ ] changed-source detection

## Production proof

- [ ] crash matrix
- [ ] disk-full matrix
- [ ] traversal corpus
- [ ] multi-receiver race
- [ ] expiry and lease race
- [ ] 10k or more tiny files
- [ ] large-file benchmark
- [ ] Debian 11 install
- [ ] Caddy TLS
- [ ] upgrade and rollback
- [ ] release binaries
- [ ] checksums
- [ ] evidence report
