package files

import (
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Onfire7/maitre-de-serveur/server/internal/owner"
)

// allPaths asks mergerfs which branches hold an entry (xattr
// user.mergerfs.allpaths, NUL-separated).
func allPaths(p string) []string {
	v := getxattr(p, "user.mergerfs.allpaths")
	if v == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(v, "\x00"), "\x00")
}

// branchList asks a Pool for its branches (xattr user.mergerfs.branches on
// its control file, colon-separated, a mode after "=").
func branchList(pool string) []string {
	v := getxattr(filepath.Join(pool, ".mergerfs"), "user.mergerfs.branches")
	if v == "" {
		return nil
	}
	var out []string
	for _, b := range strings.Split(v, ":") {
		if b, _, _ = strings.Cut(b, "="); b != "" {
			out = append(out, b)
		}
	}
	return out
}

func getxattr(p, name string) string {
	buf := make([]byte, 64*1024)
	n, err := unix.Lgetxattr(p, name, buf)
	if err != nil || n <= 0 {
		return ""
	}
	return string(buf[:n])
}

// sysProcAttr starts a helper in its own process group, as the owner as.
func sysProcAttr(as uint32) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Credential: owner.Credential(as)}
}
