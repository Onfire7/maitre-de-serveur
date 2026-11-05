//go:build windows

package storage

import "os"

// Nodes are Linux; the binary is built on Windows only as the Desktop's
// netboot helper (decision 92).
// FreeBytes is the free space of the filesystem holding path, 0 when it
// cannot be read.
func FreeBytes(path string) uint64 { return 0 }

func freeBytes(string) uint64 { return 0 }

// UsedBytes is the used space of the filesystem holding path.
func UsedBytes(string) uint64 { return 0 }

func usedBytes(string) uint64 { return 0 }

// ownedBy: Windows files have no Unix owner.
func ownedBy(os.FileInfo, uint32) bool { return false }

// TotalBytes is the size of the filesystem holding path; the Desktop's
// side never asks a Windows machine about a Node's disks.
func TotalBytes(path string) uint64 { return 0 }
