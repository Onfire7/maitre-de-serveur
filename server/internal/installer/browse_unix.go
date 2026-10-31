//go:build unix

package installer

import "syscall"

// fsUsage is the used and total size of the filesystem holding dir.
func fsUsage(dir string) (used, total uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return 0, 0
	}
	return (st.Blocks - st.Bfree) * uint64(st.Bsize), st.Blocks * uint64(st.Bsize)
}
