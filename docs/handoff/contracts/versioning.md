# Versioning

## Application releases

Use semantic versioning.

Pre-1.0 releases may change internals rapidly, but the manager should avoid shipping a public release until the contracts are coherent.

## Protocol

Protocol major changes require either:

- parallel endpoint and version support; or
- a coordinated client and server upgrade with an explicit incompatibility message.

## Database

Use ordered migrations with:

- applied migration table;
- transactional migration where possible;
- backup and rollback instructions for destructive changes;
- upgrade tests from the prior released schema.

## Manifest

Manifest `schema_version` changes only when interpretation changes. Additive optional fields may remain in the same version if older readers safely ignore them.

## Release metadata

Every binary reports:

```text
version
commit
build_date
protocol_version
```
