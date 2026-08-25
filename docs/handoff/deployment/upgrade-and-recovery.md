# Upgrade and Recovery

## Upgrade

```bash
sudo systemctl stop sss
sudo cp /var/lib/sss/sss.db /var/lib/sss/sss.db.pre-upgrade
sudo install -m 0755 new-sss-binary /usr/local/bin/sss
sudo systemctl start sss
sudo systemctl is-active sss
curl -fsS https://drop.example.com/readyz
```

Then run the smoke suite.

Production migrations must be automatic, ordered, and logged. Test upgrade from every supported prior release.

## Rollback

Rollback is allowed only when the prior binary understands the current schema. For destructive or forward-only migrations, restore the pre-upgrade database and matching storage snapshot.

## Reconciliation

On startup the daemon must inspect:

- database transfers not in terminal state;
- directories in staging, live, and trash;
- reservations;
- active claims.

It should produce a concise reconciliation report and remain unready until complete.

## Manual recovery tools

Required admin operations:

```bash
sss admin status
sss admin reconcile --dry-run
sss admin reconcile
sss admin cleanup --dry-run
sss admin cleanup
```

Destructive operations should require explicit confirmation or `--yes`.

## Corruption

If a committed manifest or payload is corrupt:

- stop serving that code;
- mark the transfer failed;
- move content to a quarantine or trash path;
- log transfer ID and stable error;
- do not attempt silent repair from incomplete data.

## Database loss

The filesystem alone is not a supported source for reconstructing public codes unless manifest metadata contains a validated code mapping by design. Prefer restoring a consistent database copy. Orphan live directories may be moved to trash after operator review.

## Disk emergency

1. Run `sss admin status`.
2. Let the service stop new sends automatically.
3. Run cleanup.
4. Inspect staging and trash.
5. Expand disk if needed.
6. Do not manually delete live directories while the daemon is running.
