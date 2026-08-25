# Performance and Resource Targets

These targets define shape rather than dishonest hardware-independent numbers.

## Memory

- Never buffer an entire transfer, archive, pack collection, or large file in memory.
- Memory should scale with configured concurrency and bounded buffers.
- Default server memory under a single large transfer should remain comfortably usable on a small VPS.
- Archive generation must stream.

## Large files

- Large regular files use direct raw segments.
- Hashing, disk, and network operations should pipeline where correctness permits.
- Throughput should approach the slowest available resource under test.
- Resumption must restart from the accepted offset, not byte zero.

## Small-file trees

- Do not issue one network request per small file.
- Use bounded packs.
- Materialization must be parallel enough to use available I/O and CPU but bounded.
- Test at least:
  - 10,000 tiny files;
  - 100,000 tiny files if the host supports it;
  - nested directories;
  - mixed tiny and large files.

## Local receive

- Must perform no payload network transfer.
- Must create no full second payload copy.
- Response should be dominated by local metadata lookup and lease creation.

## Server concurrency

Expose conservative internal defaults and allow server configuration for:

- concurrent uploads;
- concurrent downloads;
- verification/materialization workers;
- archive streams.

Do not expose pack thresholds and chunk sizes as ordinary user-facing CLI knobs.

## Benchmark reporting

Every result must record:

- CPU;
- RAM;
- disk type and filesystem;
- network path;
- transfer shape;
- compression ratio;
- server/client version;
- concurrency;
- wall time;
- CPU time;
- peak RSS;
- bytes read and written;
- request count.

Report bottlenecks, not only headline throughput.
