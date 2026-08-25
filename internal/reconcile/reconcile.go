// Package reconcile brings the database and filesystem back into agreement
// after a crash. It always runs before the server reports readiness.
package reconcile

import (
	"context"
	"log/slog"

	"github.com/sss/sss/internal/store/files"
	"github.com/sss/sss/internal/store/sqlite"
	"github.com/sss/sss/internal/transfer"
)

// Report summarizes one reconciliation pass.
type Report struct {
	CompletedCommits int
	FailedTransfers  int
	OrphanLive       int
	OrphanStaging    int
	FinishedDeletes  int
}

// Run reconciles storage with metadata. It is idempotent and safe to repeat.
//
// The invariant it protects is absolute: no code may ever resolve to incomplete
// data, so anything that cannot be proven complete is failed and trashed rather
// than published.
func Run(ctx context.Context, svc *transfer.Service, log *slog.Logger) (Report, error) {
	var rep Report
	store := svc.Store()
	layout := svc.Layout()

	known, err := store.AllTransferIDs(ctx)
	if err != nil {
		return rep, err
	}

	for id, state := range known {
		switch state {
		case sqlite.StateCommitted:
			if svc.LivePayloadValid(id) {
				continue
			}
			// A committed record without usable bytes must stop resolving.
			log.Warn("committed transfer has no usable payload; failing it", "transfer_id", id)
			svc.InvalidateManifest(id)
			_ = layout.ToTrash(id)
			_ = store.Purge(ctx, id)
			rep.FailedTransfers++

		case sqlite.StateCreated, sqlite.StateUploading, sqlite.StateVerifying:
			liveDir := layout.LivePath(id)
			if files.ManifestExists(liveDir) && svc.LivePayloadValid(id) {
				// The rename into live succeeded but the database transaction
				// did not; finish the commit idempotently.
				t, err := store.GetTransfer(ctx, id)
				if err != nil {
					continue
				}
				if _, err := svc.CompleteInterruptedPublish(ctx, t); err != nil {
					log.Warn("could not complete interrupted commit", "transfer_id", id, "error", err.Error())
					continue
				}
				rep.CompletedCommits++
				continue
			}
			if state == sqlite.StateVerifying {
				// Materialization was interrupted: staged segments have already
				// been consumed, so this transfer can never be completed. The
				// sender simply repeats the send.
				log.Warn("failing transfer interrupted during materialization", "transfer_id", id)
				_ = store.Fail(ctx, id, "INTERNAL")
				_ = layout.ToTrash(id)
				rep.FailedTransfers++
				continue
			}
			// Still a plain in-progress upload: leave it for the staging
			// timeout unless its staging tree is gone.
			if _, err := files.ReadManifest(layout.StagingPath(id)); err != nil {
				if !stagingExists(layout, id) {
					_ = store.Fail(ctx, id, "INTERNAL")
					rep.FailedTransfers++
				}
			}

		case sqlite.StateDeleting:
			_ = layout.ToTrash(id)
			_ = store.Purge(ctx, id)
			rep.FinishedDeletes++
		}
	}

	// Directories with no database record can never be published, so remove them.
	liveIDs, err := layout.ListLiveIDs()
	if err != nil {
		return rep, err
	}
	for _, id := range liveIDs {
		if _, ok := known[id]; !ok {
			log.Warn("trashing live directory with no metadata", "transfer_id", id)
			_ = layout.ToTrash(id)
			rep.OrphanLive++
		}
	}
	stagingIDs, err := layout.ListStagingIDs()
	if err != nil {
		return rep, err
	}
	for _, id := range stagingIDs {
		if _, ok := known[id]; !ok {
			_ = layout.RemoveStaging(id)
			rep.OrphanStaging++
		}
	}

	if _, err := layout.EmptyTrash(); err != nil {
		log.Warn("could not empty trash during reconciliation", "error", err.Error())
	}
	return rep, nil
}

func stagingExists(layout *files.Layout, id string) bool {
	ids, err := layout.ListStagingIDs()
	if err != nil {
		return false
	}
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}
