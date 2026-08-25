package public

import (
	"os"

	"github.com/sss/sss/internal/platform"
)

// safeJoin resolves a manifest-relative path inside the payload directory.
func safeJoin(base, rel string) (string, error) { return platform.SafeJoin(base, rel) }

// openFile opens a payload file for reading.
func openFile(path string) (*os.File, error) { return os.Open(path) }
