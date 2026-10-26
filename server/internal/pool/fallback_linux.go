package pool

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// IsFallback reports whether the default Pool currently runs on the OS
// partition: mergerfs's live branch list (an xattr, which Up updates in
// place) names FallbackDir. Read each time so the Console sees a Pool that
// lost its Drives, not the state at boot.
func IsFallback() bool {
	buf := make([]byte, 4096)
	n, err := unix.Getxattr(filepath.Join(DefaultPool, ".mergerfs"), "user.mergerfs.branches", buf)
	if err != nil || n <= 0 {
		return !mounted(DefaultPool)
	}
	for _, b := range strings.Split(string(buf[:n]), ":") {
		if strings.TrimRight(b, "/") == FallbackDir {
			return true
		}
	}
	return false
}
