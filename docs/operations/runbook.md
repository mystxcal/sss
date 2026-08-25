# Operations Runbook

## Daily shape

The service is intentionally boring: one process, one SQLite file, one storage
tree. Nothing needs to be tuned on a normal day.

```bash
systemctl status sss
curl -fsS http://127.0.0.1:7070/healthz      # process is alive
curl -fsS http://127.0.0.1:7070/readyz       # startup reconciliation finished
sudo -u sss sss admin status                 # operational snapshot
```

`readyz` returns 503 until reconciliation completes. That is intentional: a code
must never resolve while storage and metadata might still disagree.

## Logs

Logs are structured JSON on stdout, collected by journald:

```bash
journalctl -u sss -f
journalctl -u sss --since '1 hour ago' | grep '"level":"WARN"'
```

Every request carries a `request_id`, which is also returned in the
`X-Request-Id` header and inside error envelopes. When a user reports a failure,
ask for that ID.

Passwords and `Authorization` headers are never logged. If you ever see one,
treat it as a bug and report it.

## Disk pressure

```bash
sudo -u sss sss admin status
df -h /srv/sss
```

When usage crosses `limits.disk_high_watermark_percent`:

- new sends are rejected with HTTP 507 / `INSUFFICIENT_STORAGE`;
- existing committed downloads keep working;
- nothing partially written is ever published.

To recover space immediately:

```bash
sudo -u sss sss admin cleanup
```

This runs one janitor pass: expire due transfers, abandon stale staging, move
unpinned expired transfers to trash, and empty the trash. It is idempotent and
safe to run at any time.

If pressure persists, either lower `limits.max_ttl_minutes` usage in practice
(ask senders for shorter TTLs), raise the volume size, or set
`limits.max_transfer_bytes`.

## Expiry and deletion behavior

- TTL starts at commit, never at upload start.
- After expiry, new claims fail with 410; in-flight leases finish.
- A VPS-local receipt pins its payload for `storage.local_claim_grace_minutes`.
- Deletion is: rename `live/<shard>/<id>` into `trash/`, then delete
  recursively outside request handling.

A large delete therefore never blocks traffic and never exposes a half-deleted
payload.

## Restarting

```bash
sudo systemctl restart sss
```

Shutdown stops accepting new work and lets bounded active requests finish within
`server.shutdown_grace_seconds`. On start, reconciliation:

- completes a commit whose payload was published but whose database transaction
  did not land;
- fails a transfer interrupted during materialization;
- finishes an interrupted deletion;
- trashes live directories with no metadata and removes orphaned staging trees.

Committed codes, notes, and expiry times survive restarts unchanged.

## Backups

Payloads are ephemeral by design; there is nothing to back up. What is worth
preserving is the configuration:

```bash
sudo tar -czf sss-config-$(date +%F).tar.gz /etc/sss
```

If `/var/lib/sss/sss.db` is lost, restart the service: reconciliation trashes
live directories with no metadata, so the system returns to a consistent state
with those handoffs gone. Senders simply resend.

## Common situations

| Symptom | Cause | Action |
|---|---|---|
| 401 with `AUTH_INVALID` | wrong base password | check `SSS_PASSWORD` or the netrc entry |
| 429 with `RATE_LIMITED` | repeated auth failures from one address | wait a minute; fix the credential |
| 404 for a code the sender just sent | typo, or the sender used a different server | verify with `sss inspect` |
| 410 for a known code | TTL elapsed | resend; use a longer `--ttl` next time |
| 507 on send | disk high-watermark | `sss admin cleanup`, then check `df -h` |
| 409 `OFFSET_MISMATCH` on resume | client and server disagree on progress | rerun `sssend`; it re-reads the accepted offset |
| 409 `SOURCE_CHANGED` on resume | a source file changed mid-transfer | rerun the send from scratch |
| `readyz` stays 503 | reconciliation is still running or storage is unreadable | check `journalctl -u sss` |

## Health checks for monitoring

- Liveness: `GET /healthz` (unauthenticated).
- Readiness: `GET /readyz` (unauthenticated).
- Capacity: `sss admin status --json | jq .disk_used_percent`.

Prometheus is deliberately not implemented in v1; the local status endpoint
carries what is needed.
