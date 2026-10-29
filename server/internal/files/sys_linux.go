package files

import (
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Onfire7/maitre-de-serveur/server/internal/owner"
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

// sysProcAttr starts a helper in its own process group, as the owner as.
func sysProcAttr(as uint32) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Credential: owner.Credential(as)}
}
