// Package pool assembles the default Tier's Pool on one Node (ADR 0004: every
// Pool is MergerFS, even over one Drive). The branches are the Node's Drives
// in the default Tier, mounted by the storage manager; the data partition the
// Installer created (label maitre-data) is one of them, adopted by itself as
// the Node's built-in Drive. If there is no Drive at all the Pool is a
// directory on the OS partition so Services still work; the Console warns
// about it.
package pool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Layout on the Node.
const (
	DrivesDir = "/var/mnt/drives"
	PoolsDir  = "/var/mnt/pools"
	DataLabel = "maitre-data"
)

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func mounted(dir string) bool {
	b, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[1] == dir {
			return true
		}
	}
	return false
}

// Assemble mounts the Pool for one Tier over branches at PoolsDir/<tier>,
// updating the branch set in place when it is already mounted. A Tier with
// no branches on this Node gets no Pool here. local says how many leading
// branches are this Node's own Drives: files are created on those first
// (decision 106); the rest are peers' Pools over NFS.
// Assemble mounts (or re-shapes in place) the Pool of one Bin over its
// branches: this Node's Bin folders first, then peers' Pools. placement is
// the Bin's: local_first (first branch with room), most_free (most free
// space), most_free_percent. Idempotent; an empty branch list unmounts.
func Assemble(ctx context.Context, bin string, branches []string, local int, placement string) error {
	mount := filepath.Join(PoolsDir, bin)
	if len(branches) == 0 {
		if mounted(mount) {
			_ = exec.CommandContext(ctx, "umount", "-l", mount).Run()
		}
		return nil
	}
	if err := os.MkdirAll(mount, 0o755); err != nil {
		return err
	}
	create := CreatePolicy(placement, local < len(branches))
	if mounted(mount) {
		ctl := filepath.Join(mount, ".mergerfs")
		if out, err := exec.CommandContext(ctx, "setfattr", "-n", "user.mergerfs.branches", "-v", strings.Join(branches, ":"), ctl).CombinedOutput(); err != nil {
			return fmt.Errorf("update %s branches: %v: %s", bin, err, out)
		}
		_ = exec.CommandContext(ctx, "setfattr", "-n", "user.mergerfs.category.create", "-v", create, ctl).Run()
		return nil
	}
	opts := "allow_other,cache.files=partial,dropcacheonclose=true,category.create=" + create + ",moveonenospc=true,fsname=maitre-" + bin
	if out, err := exec.CommandContext(ctx, "mergerfs", "-o", opts, strings.Join(branches, ":"), mount).CombinedOutput(); err != nil {
		return fmt.Errorf("mergerfs %s: %v: %s", bin, err, out)
	}
	return nil
}

// CreatePolicy maps a Bin's placement to mergerfs's create policy. With
// remote branches present, local_first is "ff" (first branch with room,
// local branches come first); on local branches alone it is "mfs".
func CreatePolicy(placement string, hasRemote bool) string {
	switch placement {
	case "most_free_percent":
		return "pfrd"
	case "local_first":
		if hasRemote {
			return "ff"
		}
		return "mfs"
	default:
		return "mfs"
	}
}

// Mounted reports whether dir is a mount point.
func Mounted(dir string) bool { return mounted(dir) }
