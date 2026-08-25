package files

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
)

// WriteManifest durably writes a published manifest into a transfer directory.
func WriteManifest(transferDir string, m protocol.Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := platform.WriteFileSync(filepath.Join(transferDir, ManifestFile), data, 0o640); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return platform.SyncDir(transferDir)
}

// ReadManifest loads and revalidates a manifest from a transfer directory.
func ReadManifest(transferDir string) (protocol.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(transferDir, ManifestFile))
	if err != nil {
		return protocol.Manifest{}, err
	}
	var m protocol.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return protocol.Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if verr := m.Validate(); verr != nil {
		return protocol.Manifest{}, verr
	}
	return m, nil
}

// ManifestExists reports whether a transfer directory carries a manifest.
func ManifestExists(transferDir string) bool {
	_, err := os.Stat(filepath.Join(transferDir, ManifestFile))
	return err == nil
}
