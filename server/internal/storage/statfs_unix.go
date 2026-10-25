//go:build !windows

package storage

import "syscall"

func freeBytes(path string) uint64 {
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil {
		return 0
	}
	return fs.Bavail * uint64(fs.Bsize)
}

func usedBytes(path string) uint64 {
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil {
		return 0
	}
	return (fs.Blocks - fs.Bfree) * uint64(fs.Bsize)
}
