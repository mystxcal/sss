//go:build !windows

package platform

import (
	"os"
	"syscall"
)

// Disk reports capacity for the filesystem containing path.
func Disk(path string) (DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return DiskUsage{}, err
	}
	bs := uint64(st.Bsize)
	return DiskUsage{
		TotalBytes: st.Blocks * bs,
		// Bavail excludes the root reserve, which is what an unprivileged
		// service can actually use.
		FreeBytes: st.Bavail * bs,
	}, nil
}

// SyncDir fsyncs a directory so a rename or creation survives a crash.
func SyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems reject fsync on directories; the rename is still
		// ordered, so this is not fatal.
		if errno, ok := err.(*os.PathError); ok {
			if errno.Err == syscall.EINVAL || errno.Err == syscall.ENOTSUP {
				return nil
			}
		}
		return err
	}
	return nil
}

// SocketMode is the permission applied to the local Unix socket. Group members
// (the sss group) may use it; others may not.
const SocketMode os.FileMode = 0o660

// Umask-independent socket permissions.
func ChmodSocket(path string) error { return os.Chmod(path, SocketMode) }
