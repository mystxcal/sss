# Recommended Repository Layout

The exact package names may change, but preserve these ownership boundaries.

```text
sss/
├── cmd/
│   └── sss/
│       └── main.go
├── internal/
│   ├── app/                 command composition and dependency wiring
│   ├── api/
│   │   ├── public/          HTTPS handlers
│   │   ├── local/           Unix-socket handlers
│   │   └── middleware/      auth, request IDs, limits
│   ├── auth/                base-password hashing and verification
│   ├── cli/                 user commands and output rendering
│   ├── config/              server/client config
│   ├── protocol/            shared request/response/manifest types
│   ├── transfer/            lifecycle service and invariants
│   ├── store/
│   │   ├── sqlite/          metadata repository
│   │   └── files/           staging/live/trash operations
│   ├── upload/              simple and resumable upload behavior
│   ├── receive/             claims, segments, archive streaming
│   ├── materialize/         pack extraction and verification
│   ├── pack/                client pack planner
│   ├── integrity/           digest helpers
│   ├── admission/           disk reservations and limits
│   ├── janitor/             expiry and deletion
│   ├── reconcile/           crash recovery
│   ├── platform/            Windows/Linux filesystem differences
│   ├── clock/               injectable time
│   ├── ids/                 transfer, claim, code generation
│   └── observability/       logs and status
├── contracts/
│   ├── openapi.yaml
│   └── manifest.schema.json
├── integration/
│   ├── blackbox/
│   ├── faults/
│   └── testdata/
├── packaging/
│   ├── systemd/
│   ├── caddy/
│   ├── windows/
│   └── release/
├── docs/
│   ├── handoff/
│   ├── install/
│   └── operations/
├── scripts/
├── Makefile
├── go.mod
├── go.sum
├── AGENTS.md
└── README.md
```

## Boundary rules

### `protocol`

May contain pure models and validation. It must not import server storage or CLI rendering.

### `transfer`

Owns lifecycle invariants. HTTP handlers call it; handlers do not implement state transitions directly.

### `store`

Provides narrow repositories and atomic filesystem operations. SQL details do not leak through the application.

### `materialize`

Treat all archive and manifest input as hostile even though devices are trusted. It owns path and entry validation.

### `cli`

Owns user interaction only. It calls client/API packages and must not contain a second transfer protocol.

### `platform`

Contains the smallest possible set of OS-specific behavior. Do not scatter build tags throughout the codebase.

## Dependency direction

```text
cmd/app
  -> api/cli
      -> transfer/client services
          -> protocol + repository interfaces
              -> concrete sqlite/files implementations
```

Avoid cyclic service packages and mega-packages named `utils`, `common`, or `helpers`.

## Test placement

- Pure rules: package unit tests.
- Repository correctness: integration tests against real SQLite/temp filesystem.
- Public behavior: black-box tests through HTTP/Unix socket.
- Cross-platform semantics: native Windows and Linux jobs.
- Crash behavior: subprocess-based fault tests.
