//go:build !linux

package files

import "syscall"

// The file manager only runs on a Node (Linux); these keep the helper
// cross-compiling.
func allPaths(string) []string { return nil }

func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }
