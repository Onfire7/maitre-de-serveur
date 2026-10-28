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
	"sort"
	"strings"
	"syscall"
)

// Layout on the Node.
const (
	DrivesDir = "/var/mnt/drives"
	PoolsDir  = "/var/mnt/pools"
	// SharedDir holds, per Space, a Pool over this Node's own branches only:
	// what peers mount over NFS (decision 188). Exporting the full Pool
	// would loop, since it holds the peers' Pools, which hold ours.
	SharedDir = "/var/mnt/shared"
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
// Assemble mounts (or re-shapes in place) the Pool of one Space over its
// branches: this Node's Space folders first, then peers' Pools. placement is
// the Space's: local_first (first branch with room), most_free (most free
// space), most_free_percent, least_free, least_free_percent (decision
// 201). Idempotent; an empty branch list unmounts.
func Assemble(ctx context.Context, space string, branches []string, local int, placement string) error {
	return assemble(ctx, filepath.Join(PoolsDir, space), "maitre-"+space, branches, local, placement)
}

// AssembleShared mounts the local-only Pool of one Space at
// SharedDir/<space>, over this Node's own branches.
func AssembleShared(ctx context.Context, space string, branches []string, placement string) error {
	return assemble(ctx, filepath.Join(SharedDir, space), "maitre-shared-"+space, branches, len(branches), placement)
}

func assemble(ctx context.Context, mount, fsname string, branches []string, local int, placement string) error {
	space := filepath.Base(mount)
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
	if placement == "least_free_percent" {
		// mergerfs has no such policy: order the branches fullest first
		// (by share free) and take the first with room. The order is
		// refreshed whenever the Pool is assembled; the fullest Drive
		// stays fullest as it fills, so it rarely needs to change.
		branches = ByFreeShare(branches, freeShare)
	}
	if mounted(mount) {
		ctl := filepath.Join(mount, ".mergerfs")
		if out, err := exec.CommandContext(ctx, "setfattr", "-n", "user.mergerfs.branches", "-v", strings.Join(branches, ":"), ctl).CombinedOutput(); err != nil {
			return fmt.Errorf("update %s branches: %v: %s", space, err, out)
		}
		_ = exec.CommandContext(ctx, "setfattr", "-n", "user.mergerfs.category.create", "-v", create, ctl).Run()
		return nil
	}
	opts := "allow_other,cache.files=partial,dropcacheonclose=true,category.create=" + create + ",moveonenospc=true,fsname=" + fsname
	if out, err := exec.CommandContext(ctx, "mergerfs", "-o", opts, strings.Join(branches, ":"), mount).CombinedOutput(); err != nil {
		return fmt.Errorf("mergerfs %s: %v: %s", space, err, out)
	}
	return nil
}

// CreatePolicy maps a Space's placement to mergerfs's create policy. With
// remote branches present, local_first is "ff" (first branch with room,
// local branches come first); on local branches alone it is "mfs".
func CreatePolicy(placement string, hasRemote bool) string {
	switch placement {
	case "most_free_percent":
		return "pfrd"
	case "least_free":
		return "lfs"
	case "least_free_percent":
		return "ff"
	case "local_first":
		if hasRemote {
			return "ff"
		}
		return "mfs"
	default:
		return "mfs"
	}
}

// ByFreeShare orders branches by their share of free space, least first;
// a branch whose share cannot be read goes last. Stable for equal shares.
func ByFreeShare(branches []string, share func(string) (float64, bool)) []string {
	out := append([]string(nil), branches...)
	key := map[string]float64{}
	for _, b := range out {
		if f, ok := share(b); ok {
			key[b] = f
		} else {
			key[b] = 2
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return key[out[i]] < key[out[j]] })
	return out
}

// freeShare is the share of a branch's filesystem free to unprivileged
// writers, 0 to 1.
func freeShare(dir string) (float64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil || st.Blocks == 0 {
		return 0, false
	}
	return float64(st.Bavail) / float64(st.Blocks), true
}

// Mounted reports whether dir is a mount point.
func Mounted(dir string) bool { return mounted(dir) }
