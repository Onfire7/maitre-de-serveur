//go:build !windows

package storage

import "syscall"

// FreeBytes is the free space of the filesystem holding path, 0 when it
// cannot be read.
func FreeBytes(path string) uint64 { return freeBytes(path) }

func freeBytes(path string) uint64 {
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil {
		return 0
	}
	return fs.Bavail * uint64(fs.Bsize)
}

// UsedBytes is the used space of the filesystem holding path.
func UsedBytes(path string) uint64 { return usedBytes(path) }

func usedBytes(path string) uint64 {
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil {
		return 0
	}
	return (fs.Blocks - fs.Bfree) * uint64(fs.Bsize)
}
