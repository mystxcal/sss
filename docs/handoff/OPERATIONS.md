# Operations

## Service topology

```text
Internet
  -> Caddy :443
     -> sssd 127.0.0.1:7070

VPS local agents
  -> /run/sss/sssd.sock

sssd
  -> /var/lib/sss/sss.db
  -> /srv/sss/{staging,live,trash}
```

## Service identity

Recommended:

```text
user:  sss
group: sss
```

VPS-local agents that need path access join the `sss` group.

Committed directories and files should be owned by the service and read-only to the local group.

## Health

- `/healthz`: process is alive.
- `/readyz`: configuration loaded, database open, migrations complete, storage writable, reconciliation complete.
- `sss doctor`: client-to-server checks.
- `sss admin status`: storage and lifecycle summary.

## Status must expose

- version;
- uptime;
- live transfer count and bytes;
- staging transfer count and bytes;
- active claims;
- reserved bytes;
- filesystem free bytes;
- high-watermark status;
- reclaimable and trash bytes;
- oldest staging item;
- last cleanup result;
- reconciliation status.

## Logs

Structured logs should include timestamp, level, request ID, operation, transfer or claim ID when relevant, bytes, duration, and stable error code.

Never log:

- Authorization;
- base password;
- claim bearer token;
- full note by default;
- arbitrary payload contents.

## Backups

Payloads are ephemeral and do not require backup.

The SQLite database can be backed up for operational continuity, but restore semantics must not invent live records without matching filesystem content. Prefer a consistent SQLite backup plus storage snapshot if backup is desired.

## Upgrade

1. Stop accepting new traffic through readiness or drain.
2. Gracefully stop daemon.
3. Back up database and config if desired.
4. Replace binary.
5. Start daemon.
6. Run migrations and reconciliation.
7. Verify readiness.
8. Run smoke tests.

Schema migrations must be forward-safe and tested on a copy of prior-version state.

## Password rotation

1. Generate a new password hash.
2. Update config atomically.
3. Restart or reload.
4. Update trusted clients or netrc.
5. Existing transfers remain valid but require the new password for new public requests.

## Recovery cases

### Database present, live directory missing

Mark transfer failed or deleted after reconciliation. Never serve it.

### Live directory present, database pre-commit

Validate manifest and finish or roll back idempotently.

### Trash directory remains after crash

Resume deletion.

### Staging item exceeds internal timeout

Mark abandoned and move to trash unless an active resumable session is valid.

### Disk high watermark

Reject new sends, retain receives, run cleanup, and report status.

See [`deployment/upgrade-and-recovery.md`](deployment/upgrade-and-recovery.md).
