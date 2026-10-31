//go:build !windows

package copies

import (
	"os"
	"syscall"
)

// chownLike gives path the owner info has.
func chownLike(path string, info os.FileInfo) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(path, int(st.Uid), int(st.Gid))
	}
}
