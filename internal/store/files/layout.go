// Package files owns the staging, live, and trash directories. All three share
// one filesystem so that publication and deletion are directory renames.
package files

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sss/sss/internal/platform"
)

// Standard directory and file names inside a transfer directory.
const (
	PayloadDir   = "payload"
	SegmentsDir  = "segments"
	PacksDir     = "packs"
	ManifestFile = "manifest.json"
	StagingFile  = "transfer.json"
	// OutboardsDir holds one BLAKE3 verification tree per segment. It is
	// deliberately not inside PayloadDir, which a local receiver is handed
	// directly, nor inside SegmentsDir, which materialization requires to end
	// up empty.
	OutboardsDir = "outboards"
	// OutboardSuffix names a verification tree after its segment.
	OutboardSuffix = ".obao"
)

// Layout resolves storage paths under a single data directory.
type Layout struct {
	DataDir string
}

// New returns a layout rooted at dataDir and creates the top-level directories.
func New(dataDir string) (*Layout, error) {
	l := &Layout{DataDir: dataDir}
	for _, d := range []string{l.Staging(), l.Live(), l.Trash()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	return l, nil
}

// Staging is the root of in-progress transfers.
func (l *Layout) Staging() string { return filepath.Join(l.DataDir, "staging") }

// Live is the root of committed, immutable transfers.
func (l *Layout) Live() string { return filepath.Join(l.DataDir, "live") }

// Trash holds directories awaiting recursive deletion.
func (l *Layout) Trash() string { return filepath.Join(l.DataDir, "trash") }

// Shard returns the two-character directory that spreads live transfers out.
func Shard(transferID string) string {
	trimmed := strings.TrimPrefix(transferID, "t-")
	if len(trimmed) < 2 {
		return "00"
	}
	return trimmed[:2]
}

// StagingPath is the staging directory for a transfer.
func (l *Layout) StagingPath(transferID string) string {
	return filepath.Join(l.Staging(), transferID)
}

// LivePath is the committed directory for a transfer.
func (l *Layout) LivePath(transferID string) string {
	return filepath.Join(l.Live(), Shard(transferID), transferID)
}

// TrashPath is the trash directory for a transfer.
func (l *Layout) TrashPath(transferID string) string {
	return filepath.Join(l.Trash(), transferID)
}

// PrepareStaging creates a staging tree for a new transfer.
func (l *Layout) PrepareStaging(transferID string) (string, error) {
	root := l.StagingPath(transferID)
	for _, d := range []string{root, filepath.Join(root, PayloadDir), filepath.Join(root, SegmentsDir), filepath.Join(root, PacksDir)} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return "", fmt.Errorf("create staging directory: %w", err)
		}
	}
	return root, nil
}

// Publish atomically moves a verified staging directory into live.
func (l *Layout) Publish(transferID string) (string, error) {
	src := l.StagingPath(transferID)
	dst := l.LivePath(transferID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", fmt.Errorf("create live shard: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		// A prior attempt already published these bytes; treat as done so the
		// database step can be retried idempotently.
		return dst, nil
	}
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("publish transfer: %w", err)
	}
	if err := platform.SyncDir(filepath.Dir(dst)); err != nil {
		return "", fmt.Errorf("sync live shard: %w", err)
	}
	return dst, nil
}

// ToTrash atomically moves any transfer directory into trash. It is idempotent.
func (l *Layout) ToTrash(transferID string) error {
	dst := l.TrashPath(transferID)
	for _, src := range []string{l.LivePath(transferID), l.StagingPath(transferID)} {
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			// Trash slot taken by an earlier pass: delete what is there first.
			if err := platform.RemoveAllForce(dst); err != nil {
				return err
			}
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move %s to trash: %w", transferID, err)
		}
	}
	return nil
}

// EmptyTrash deletes every trashed directory and returns how many were removed.
func (l *Layout) EmptyTrash() (int, error) {
	entries, err := os.ReadDir(l.Trash())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		p := filepath.Join(l.Trash(), e.Name())
		if err := platform.RemoveAllForce(p); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// ListStagingIDs returns the transfer IDs that currently have staging trees.
func (l *Layout) ListStagingIDs() ([]string, error) {
	entries, err := os.ReadDir(l.Staging())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// ListLiveIDs returns the transfer IDs that currently have live trees.
func (l *Layout) ListLiveIDs() ([]string, error) {
	shards, err := os.ReadDir(l.Live())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(l.Live(), shard.Name()))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, e.Name())
			}
		}
	}
	return out, nil
}

// RemoveStaging deletes a staging tree outright.
func (l *Layout) RemoveStaging(transferID string) error {
	return platform.RemoveAllForce(l.StagingPath(transferID))
}
