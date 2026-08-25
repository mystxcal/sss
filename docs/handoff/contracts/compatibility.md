# Compatibility Contract

## Protocol version

Public JSON includes a protocol version:

```text
major.minor
```

- Major: incompatible contract change.
- Minor: backward-compatible capability addition.

Clients call `/v1/info` and reject unsupported major versions.

## Server and client tolerance

- Ignore unknown JSON fields.
- Do not silently reinterpret known fields.
- Required capabilities are explicit.
- Advanced transfer creation negotiates segment kinds and digest algorithms.
- Simple curl endpoints remain stable across minor versions.

## Manifest version

Manifest uses integer `schema_version`.

A server must reject a manifest version it cannot validate.

## Filesystem portability

Portable v1 entries:

- directories;
- regular files;
- modification time;
- executable bit.

Not portable or supported:

- symlinks;
- hardlink identity;
- ACLs;
- owners and groups;
- device files;
- sockets and FIFOs;
- extended attributes;
- alternate data streams.

Paths use `/` in manifests regardless of client OS.

The client maps paths safely to the native filesystem and rejects impossible names rather than silently changing them.

## Code compatibility

Codes are normalized by removing hyphens and ASCII whitespace, then uppercasing. Exactly eight valid alphabet characters must remain.
