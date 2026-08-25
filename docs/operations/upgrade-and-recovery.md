# Upgrade and Recovery

## Upgrading

Handoffs are ephemeral, so upgrades do not need a maintenance window beyond a
short restart. Anything in flight at the moment of restart is simply resent.

```bash
# 1. Note the running version so a rollback is unambiguous.
sss version
sudo -u sss sss admin status

# 2. Stage the new binary next to the old one.
sudo install -m 0755 sss-<new-version>-linux-amd64 /usr/local/bin/sss.new

# 3. Verify it starts and reports the expected version.
/usr/local/bin/sss.new version

# 4. Swap and restart.
sudo cp /usr/local/bin/sss /usr/local/bin/sss.previous
sudo mv /usr/local/bin/sss.new /usr/local/bin/sss
sudo systemctl restart sss

# 5. Confirm.
curl -fsS http://127.0.0.1:7070/readyz
sudo -u sss sss admin status
SSS_URL=https://drop.example.com SSS_PASSWORD=... scripts/contract-smoke.sh
```

Database migrations are ordered and applied inside a transaction at startup.
The applied set is recorded in `schema_migrations`.

## Rolling back

```bash
sudo systemctl stop sss
sudo mv /usr/local/bin/sss.previous /usr/local/bin/sss
sudo systemctl start sss
curl -fsS http://127.0.0.1:7070/readyz
```

A rollback is safe as long as the newer version did not apply a migration the
older one does not know. Before shipping any migration, take a copy first:

```bash
sudo systemctl stop sss
sudo cp /var/lib/sss/sss.db /var/lib/sss/sss.db.pre-upgrade
sudo systemctl start sss
```

Restore by stopping the service, moving that file back, and starting again.
Reconciliation will trash any live directory the restored database does not
know about.

## Recovery scenarios

### The daemon was killed mid-upload

Staging data is never addressable by a code. The staging timeout
(`storage.staging_ttl_minutes`) abandons it, and the janitor removes it. No code
was ever issued.

### The daemon was killed during commit

Two cases, both handled at startup:

- The payload had been renamed into `live` but the database transaction had not
  landed: reconciliation validates the manifest and completes the commit
  idempotently, allocating the code then.
- Materialization was interrupted: the transfer is marked failed and trashed,
  because its staged segments were already consumed. The sender resends.

### The database file is lost or corrupt

```bash
sudo systemctl stop sss
sudo mv /var/lib/sss/sss.db /var/lib/sss/sss.db.broken
sudo systemctl start sss
```

The service recreates the schema, then reconciliation trashes every live
directory that has no metadata. Existing codes stop working; senders resend.
This is acceptable precisely because handoffs are ephemeral.

### Storage is full

```bash
sudo -u sss sss admin cleanup
df -h /srv/sss
```

New sends are refused with `INSUFFICIENT_STORAGE` until space is available.
Committed downloads keep working throughout.

### A stale socket blocks startup

If the daemon was killed hard, `/run/sss/sssd.sock` may remain. The daemon
removes a stale socket automatically and refuses to start only when another
process is actually listening on it. Under systemd, `RuntimeDirectory=sss`
clears the directory on stop.

### Verifying a payload by hand

Every committed transfer carries its manifest:

```bash
sudo -u sss cat /srv/sss/live/<shard>/<transfer-id>/manifest.json | jq .
```

Entries hold BLAKE3-256 digests of the materialized files. The payload tree is
read-only by design; do not chmod it.
