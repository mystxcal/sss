//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows"
)

// Disk reports capacity for the volume containing path.
func Disk(path string) (DiskUsage, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return DiskUsage{}, err
	}
	var freeToCaller, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &free); err != nil {
		return DiskUsage{}, err
	}
	return DiskUsage{TotalBytes: total, FreeBytes: freeToCaller}, nil
}

// SyncDir is a no-op on Windows, where directory handles are not fsynced.
func SyncDir(path string) error { return nil }

// SocketMode is unused on Windows; the daemon targets Linux.
const SocketMode os.FileMode = 0o600

// ChmodSocket is a no-op on Windows.
func ChmodSocket(path string) error { return nil }
