// Package platform holds the smallest possible set of OS-specific behavior:
// filesystem statistics, durable directory operations, and read-only payload
// enforcement. Build-tagged files stay confined to this package.
package platform

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DiskUsage describes a filesystem's capacity.
type DiskUsage struct {
	TotalBytes uint64
	FreeBytes  uint64
}

// UsedPercent returns whole-percent utilization, or 0 when unknown.
func (d DiskUsage) UsedPercent() int {
	if d.TotalBytes == 0 {
		return 0
	}
	used := d.TotalBytes - d.FreeBytes
	return int((used * 100) / d.TotalBytes)
}

// MkdirAllSync creates a directory tree and fsyncs the deepest new directory.
func MkdirAllSync(path string, perm os.FileMode) error {
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	return SyncDir(path)
}

// WriteFileSync writes a file durably: temp file, fsync, rename, fsync parent.
func WriteFileSync(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return SyncDir(dir)
}

// CopyFile copies a regular file, preserving the executable bit.
func CopyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(out, in, buf); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// CopyTree copies a directory tree, rejecting anything that is not a regular
// file or directory.
func CopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			perm := os.FileMode(0o644)
			if info.Mode().Perm()&0o111 != 0 {
				perm = 0o755
			}
			return CopyFile(p, target, perm)
		default:
			return errors.New("unsupported file type in payload: " + rel)
		}
	})
}

// MakeTreeReadOnly removes write bits from every file and directory in a tree
// so a committed payload cannot be modified in place.
func MakeTreeReadOnly(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() &^ 0o222
		return os.Chmod(p, mode)
	})
	if err != nil {
		return err
	}
	// Directories are locked last, deepest first, so earlier chmods still work.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			return err
		}
	}
	return nil
}

// SealPayload makes a committed transfer's contents immutable while leaving the
// transfer directory itself writable.
//
// The root must stay writable: moving a directory to a new parent updates its
// own ".." entry, so rename(2) requires write permission on the directory being
// moved. Sealing the root would make both publication (staging -> live) and
// deletion (live -> trash) fail with EACCES for any non-root service user.
func SealPayload(transferDir string) error {
	entries, err := os.ReadDir(transferDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(transferDir, e.Name())
		if e.IsDir() {
			if err := MakeTreeReadOnly(p); err != nil {
				return err
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if err := os.Chmod(p, info.Mode().Perm()&^0o222); err != nil {
			return err
		}
	}
	return nil
}

// MakeTreeWritable restores write permission, which deletion needs.
func MakeTreeWritable(root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return os.Chmod(p, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		return os.Chmod(p, info.Mode().Perm()|0o200)
	})
}

// RemoveAllForce restores write bits before recursive deletion.
func RemoveAllForce(root string) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	if err := MakeTreeWritable(root); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.RemoveAll(root)
}

// SafeJoin joins a slash-separated relative path onto a base directory and
// guarantees the result stays inside that base.
func SafeJoin(base, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("empty relative path")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", errors.New("absolute path is not allowed: " + rel)
	}
	cleanBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(cleanBase, filepath.FromSlash(rel))
	cleaned := filepath.Clean(joined)
	if cleaned != cleanBase && !strings.HasPrefix(cleaned, cleanBase+string(os.PathSeparator)) {
		return "", errors.New("path escapes its root: " + rel)
	}
	return cleaned, nil
}

// UniqueDestination returns dst when free, otherwise dst-2, dst-3, and so on.
func UniqueDestination(dst string) (string, error) {
	if _, err := os.Lstat(dst); os.IsNotExist(err) {
		return dst, nil
	} else if err != nil {
		return "", err
	}
	for i := 2; i < 1000; i++ {
		candidate := dst + "-" + itoa(i)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", errors.New("could not find a free destination near " + dst)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
