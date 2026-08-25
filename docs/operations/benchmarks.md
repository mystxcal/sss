# Benchmark Evidence

These numbers describe shape, not marketing throughput: the point is that memory
stays bounded, request counts stay bounded, and the local path copies nothing.

Reproduce with the commands shown; report bottlenecks, not just headline speed.

## Environment

| Property | Value |
|---|---|
| CPU | 6 vCPU, x86-64 |
| RAM | 25 GiB |
| Disk / filesystem | virtual disk, ext4 |
| Network path | loopback (isolates client/server cost from the network) |
| OS | Debian GNU/Linux, kernel 5.10 |
| Go | 1.26.4, `CGO_ENABLED=0` |
| Server / client version | 1.0.0 (protocol 1.0) |
| Server concurrency | 8 uploads, 16 downloads, 4 materialize workers |
| Client concurrency | 4 segment transfers |

## Mixed tree: 10,000 tiny files plus one 200 MiB file

```bash
# 10,000 files of ~14 bytes across 50 directories, plus one 200 MiB random file
python3 -c "..." # see the runbook; any generator works
head -c 200000000 /dev/urandom > bench/big.bin

CODE=$(sss send bench --quiet --ttl 60)
sss recv "$CODE" --no-local --to received --quiet
```

| Metric | Result |
|---|---|
| Payload | 10,001 files, 231 MiB total |
| Send wall time | 12.4 s |
| Receive wall time | 9.5 s |
| HTTP requests for the whole send | 7 |
| HTTP requests for the whole receive | 4 |
| Server peak RSS during both | 47 MiB |
| Byte-for-byte verification | `diff -r` clean |

The request counts are the important result. The 10,000 small files travel as
bounded `tar.zst` packs, and the 200 MiB file as one resumable raw segment, so
the send is one create, four segment uploads, one commit, and one metadata call
— not 10,001 requests. The receive is one claim, two segment downloads, and one
completion.

Server memory stayed at 47 MiB while a 200 MiB file passed through it, which is
the guarantee that matters: memory scales with configured concurrency and buffer
sizes, never with transfer size. Archives are streamed for the same reason —
`GET /r/{code}?format=zip` never builds a second copy on disk.

The dominant cost on both sides is hashing and compression (BLAKE3 over every
file, zstd over the packs), not the transport.

## VPS-local receive

```bash
du -sb /srv/sss                                        # before
curl -fsS --unix-socket /run/sss/sssd.sock \
  http://localhost/local/r/$CODE                       # returns a path
du -sb /srv/sss                                        # after
```

| Metric | Result |
|---|---|
| Payload bytes transferred | 0 |
| New bytes written to storage | 0 (verified by the black-box test) |
| Response | the existing read-only `live/<shard>/<id>/payload` path |

`integration/blackbox/local_test.go` asserts this continuously: it measures the
storage tree before and after a local receive and fails if it grows.

## Resumability

`integration/blackbox/resumable_test.go` interrupts a segment at an exact
offset, confirms `HEAD` reports the accepted offset, rejects a wrong offset with
`OFFSET_MISMATCH`, then finishes from that offset. Resumption restarts from the
accepted byte, never from zero.

## What is not measured here

Real WAN throughput, spinning disks, and Windows native I/O will differ. Re-run
these commands on the deployment host before quoting numbers to anyone.
