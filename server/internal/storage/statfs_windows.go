//go:build windows

package storage

// Nodes are Linux; the binary is built on Windows only as the Desktop's
// netboot helper (decision 92).
// FreeBytes is the free space of the filesystem holding path, 0 when it
// cannot be read.
func FreeBytes(path string) uint64 { return 0 }

func freeBytes(string) uint64 { return 0 }

// UsedBytes is the used space of the filesystem holding path.
func UsedBytes(string) uint64 { return 0 }

func usedBytes(string) uint64 { return 0 }
