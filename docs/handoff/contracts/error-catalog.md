# Error Catalog

| Code | HTTP | CLI exit | Meaning | Retry |
|---|---:|---:|---|---|
| `AUTH_REQUIRED` | 401 | 3 | No credential supplied | after credential |
| `AUTH_INVALID` | 401 | 3 | Credential rejected | after credential |
| `RATE_LIMITED` | 429 | 5 | Too many attempts or requests | after delay |
| `INVALID_REQUEST` | 400 | 2 | Malformed request | fix request |
| `INVALID_CODE` | 400 | 2 | Code syntax invalid | no |
| `TRANSFER_NOT_FOUND` | 404 | 4 | No committed transfer for code | verify code |
| `TRANSFER_EXPIRED` | 410 | 4 | Code expired | no |
| `TTL_OUT_OF_RANGE` | 422 | 2 | TTL outside 1 through 3000 minutes | fix request |
| `NO_FILES` | 422 | 2 | Send contained no file | fix request |
| `UNSUPPORTED_ENTRY` | 422 | 7 | Symlink, device, or similar rejected | no |
| `INVALID_PATH` | 422 | 7 | Path unsafe or not portable | no |
| `DUPLICATE_PATH` | 422 | 7 | Two entries normalize to same path | no |
| `SOURCE_CHANGED` | 409 | 6 | Source changed during or before resume | restart transfer |
| `OFFSET_MISMATCH` | 409 | 6 | Resumable offset differs | query offset |
| `STATE_CONFLICT` | 409 | 6 | Operation invalid for transfer state | inspect or retry |
| `IDEMPOTENCY_CONFLICT` | 409 | 6 | Same key used for different request | new key |
| `PAYLOAD_TOO_LARGE` | 413 | 7 | Configured size limit exceeded | reduce or split |
| `TOO_MANY_FILES` | 413 | 7 | File-count limit exceeded | reduce or split |
| `INSUFFICIENT_STORAGE` | 507 | 7 | Disk admission denied or disk full | after cleanup |
| `HASH_MISMATCH` | 422 | 6 | Integrity verification failed | retry or new transfer |
| `CLAIM_EXPIRED` | 410 | 4 | Receive session lease ended | new claim if code valid |
| `DESTINATION_EXISTS` | n/a | 7 | Client destination already exists | choose path |
| `NETWORK_ERROR` | n/a | 5 | Client could not complete HTTP operation | retry or resume |
| `PROTOCOL_MISMATCH` | 426 | 5 | Client and server versions incompatible | upgrade |
| `INTERNAL` | 500 | 8 | Unexpected server failure | retry and report request ID |

Messages may improve; codes and meanings are stable within a protocol major version.
