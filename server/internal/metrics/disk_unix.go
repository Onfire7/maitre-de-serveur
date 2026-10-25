//go:build !windows

package metrics

import "syscall"

func disk(dir string) (total, used uint64) {
	var st syscall.Statfs_t
	if dir == "" || syscall.Statfs(dir, &st) != nil {
		return 0, 0
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	if total > free {
		used = total - free
	}
	return total, used
}
