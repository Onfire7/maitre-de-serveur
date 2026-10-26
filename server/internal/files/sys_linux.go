package files

import (
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// allPaths asks mergerfs which branches hold an entry (xattr
// user.mergerfs.allpaths, NUL-separated).
func allPaths(p string) []string {
	buf := make([]byte, 64*1024)
	n, err := unix.Lgetxattr(p, "user.mergerfs.allpaths", buf)
	if err != nil || n <= 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(buf[:n]), "\x00"), "\x00")
}

func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
