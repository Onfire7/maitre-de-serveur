//go:build windows

package storage

// Nodes are Linux; the binary is built on Windows only as the Desktop's
// netboot helper (decision 92).
func freeBytes(string) uint64 { return 0 }
func usedBytes(string) uint64 { return 0 }
