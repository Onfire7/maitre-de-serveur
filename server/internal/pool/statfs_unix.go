//go:build !windows

package pool

import "syscall"

// statBranch reads a branch's free space for unprivileged writers.
func statBranch(dir string) (uint64, float64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil || st.Blocks == 0 {
		return 0, 0, false
	}
	return st.Bavail * uint64(st.Bsize), float64(st.Bavail) / float64(st.Blocks), true
}
