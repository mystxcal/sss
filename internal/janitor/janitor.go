// Package janitor enforces expiry and deletion. Deletion is a rename into
// trash followed by recursive removal outside any request handler, so a large
// delete never blocks traffic and never leaves partially visible live content.
package janitor

import (
	"context"
	"log/slog"
	"time"

	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/store/sqlite"
	"github.com/sss/sss/internal/transfer"
)

// Janitor runs expiry and cleanup passes.
type Janitor struct {
	svc *transfer.Service
	log *slog.Logger
}

// New builds a janitor.
func New(svc *transfer.Service, log *slog.Logger) *Janitor {
	return &Janitor{svc: svc, log: log}
}

// Run sweeps until the context is cancelled.
func (j *Janitor) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = transfer.SweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := j.Sweep(ctx); err != nil {
				j.log.Warn("cleanup pass failed", "error", err.Error())
			}
		}
	}
}

// Sweep performs one idempotent cleanup pass.
func (j *Janitor) Sweep(ctx context.Context) (protocol.CleanupResult, error) {
	var result protocol.CleanupResult
	store := j.svc.Store()
	layout := j.svc.Layout()
	cfg := j.svc.Config()
	now := j.svc.Now()

	expired, err := store.ExpireDue(ctx, now)
	if err != nil {
		return result, err
	}
	result.Expired = expired

	// Abandon staged transfers that outlived the internal staging timeout. This
	// is independent of the public TTL, which only starts at commit.
	stale, err := store.StaleStaging(ctx, now.Add(-time.Duration(cfg.Storage.StagingTTLMinutes)*time.Minute))
	if err != nil {
		return result, err
	}
	for _, t := range stale {
		if err := store.SetState(ctx, t.ID, sqlite.StateAbandoned); err != nil {
			j.log.Warn("could not abandon stale transfer", "transfer_id", t.ID, "error", err.Error())
			continue
		}
		result.StagingCleaned++
	}

	// Delete anything expired or terminal that no lease still pins.
	unpinned, err := store.UnpinnedExpired(ctx, now)
	if err != nil {
		return result, err
	}
	for _, t := range unpinned {
		if err := store.SetState(ctx, t.ID, sqlite.StateDeleting); err != nil {
			j.log.Warn("could not mark transfer deleting", "transfer_id", t.ID, "error", err.Error())
			continue
		}
		j.svc.InvalidateManifest(t.ID)
		if err := layout.ToTrash(t.ID); err != nil {
			j.log.Warn("could not move transfer to trash", "transfer_id", t.ID, "error", err.Error())
			continue
		}
		if err := store.Purge(ctx, t.ID); err != nil {
			j.log.Warn("could not purge transfer record", "transfer_id", t.ID, "error", err.Error())
			continue
		}
		result.Deleted++
		j.log.Info("transfer deleted", "transfer_id", t.ID, "previous_state", t.State)
	}

	emptied, err := layout.EmptyTrash()
	if err != nil {
		j.log.Warn("could not empty trash", "error", err.Error())
	}
	result.TrashEmptied = emptied

	if err := store.PurgeExpiredIdempotency(ctx, now); err != nil {
		j.log.Warn("could not purge idempotency keys", "error", err.Error())
	}
	return result, nil
}
