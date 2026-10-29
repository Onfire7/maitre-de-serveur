// Package owner gives a Storage Space's shared folders their Space's owner
// (decisions 173, 182): one id as user and group, folders setgid 2775 with
// a default ACL giving the group rwx, so whatever umask a container uses,
// the other containers in the group can still write.
package owner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Marker records in a shared folder which owner it was last given, so a
// folder mounted again is not walked again.
const Marker = ".maitre-owner"

// Chown and ACL are swapped in tests; ACL runs setfacl with its arguments.
var (
	Chown = os.Lchown
	ACL   = func(args ...string) error { return exec.Command("setfacl", args...).Run() }
)

// Apply gives one file or folder the owner id; 0 does nothing.
func Apply(path string, id uint32) error {
	if id == 0 {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err := Chown(path, int(id), int(id)); err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := os.Chmod(path, 0o775|fs.ModeSetgid); err != nil {
			return err
		}
		// Default ACL: new entries get group rwX whatever the umask.
		return ACL("-d", "-m", "g::rwX,m::rwX", path)
	case info.Mode().IsRegular():
		return os.Chmod(path, info.Mode().Perm()|0o060)
	}
	return nil
}

// Admit lets the Space's owner into a Service's own folder (decision 216):
// an ACL entry, and a default one so folders made in it later carry it too,
// so the file manager, which acts as that owner (decision 189), can move and
// upload files into it. Owner and mode stay as the container has them; 0
// does nothing. Only the folder itself: walking a tree the container writes
// to as root could be led out of it by a symlink.
func Admit(dir string, id uint32) error {
	if id == 0 {
		return nil
	}
	return ACL("-m", fmt.Sprintf("u:%d:rwX,d:u:%d:rwX", id, id), dir)
}

// Tree applies the owner to root and everything under it, skipping
// Maître's own hidden files.
func Tree(root string, id uint32) error {
	if id == 0 {
		return nil
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // vanished or unreadable: carry on
		}
		if p != root && strings.HasPrefix(d.Name(), ".maitre") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return Apply(p, id)
	})
}

var (
	mu      sync.Mutex
	running = map[string]bool{}
)

// Share makes sure dir exists and belongs to id, for a folder picked for a
// Service (170, 171). Missing folders are made at once; an existing tree
// not yet given this owner is walked in the background (a large media
// library takes a while) and marked when done. done, if set, runs then.
func Share(ctx context.Context, dir string, id uint32, done func(error)) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := makeAll(dir, id); err != nil {
			return err
		}
	}
	if id == 0 {
		return nil
	}
	want := strconv.FormatUint(uint64(id), 10)
	if b, err := os.ReadFile(filepath.Join(dir, Marker)); err == nil && strings.TrimSpace(string(b)) == want {
		return nil
	}
	mu.Lock()
	if running[dir] {
		mu.Unlock()
		return nil
	}
	running[dir] = true
	mu.Unlock()
	go func() {
		err := Tree(dir, id)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, Marker), []byte(want+"\n"), 0o644)
		}
		mu.Lock()
		delete(running, dir)
		mu.Unlock()
		if done != nil {
			done(err)
		}
	}()
	return nil
}

// makeAll is MkdirAll that gives each folder it makes the owner.
func makeAll(dir string, id uint32) error {
	parent := filepath.Dir(dir)
	if _, err := os.Stat(parent); os.IsNotExist(err) && parent != dir {
		if err := makeAll(parent, id); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0o775); err != nil && !os.IsExist(err) {
		return err
	}
	return Apply(dir, id)
}

// Credential is the identity As gives file operations, for a child
// process's SysProcAttr; nil for id 0 or an agent that is not root.
func Credential(id uint32) *syscall.Credential {
	if id == 0 || os.Geteuid() != 0 {
		return nil
	}
	return &syscall.Credential{Uid: id, Gid: id, Groups: []uint32{}}
}
