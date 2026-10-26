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
	DrivesDir   = "/var/mnt/drives"
	PoolsDir    = "/var/mnt/pools"
	DefaultPool = PoolsDir + "/default"
	// FallbackDir backs the default Pool when the Node has no Drive.
	FallbackDir = "/var/maitre/pool-fallback"
	DataLabel   = "maitre-data"
)

// Status describes the assembled Pool.
type Status struct {
	Mount    string
	Branches []string
	Fallback bool
}

// Up assembles the default Pool over the mounted branch directories given
// (the default Tier's Drives). Idempotent: safe to run at every boot.
func Up(ctx context.Context, extra ...string) (*Status, error) {
	var branches []string
	for _, e := range extra {
		if mounted(e) {
			branches = append(branches, e)
		}
	}
	st := &Status{Mount: DefaultPool, Branches: branches}
	if len(branches) == 0 {
		st.Fallback = true
		if err := os.MkdirAll(FallbackDir, 0o755); err != nil {
			return nil, err
		}
		branches = []string{FallbackDir}
	}
	if err := os.MkdirAll(DefaultPool, 0o755); err != nil {
		return nil, err
	}
	if mounted(DefaultPool) {
		// Already assembled: make the branch set match without a remount
		// (mergerfs takes the list through an xattr; open files survive).
		if out, err := exec.CommandContext(ctx, "setfattr", "-n", "user.mergerfs.branches", "-v", strings.Join(branches, ":"), filepath.Join(DefaultPool, ".mergerfs")).CombinedOutput(); err != nil {
			return st, fmt.Errorf("update pool branches: %v: %s", err, out)
		}
		return st, nil
	}
	// mergerfs over the branches. Options follow the usual media-server
	// recipe: create new files on the branch with most free space, keep
	// directory paths consistent, and use the pool through fuse without
	// caching surprises for databases.
	opts := "allow_other,cache.files=partial,dropcacheonclose=true,category.create=mfs,moveonenospc=true,fsname=maitre-default"
	if out, err := exec.CommandContext(ctx, "mergerfs", "-o", opts, strings.Join(branches, ":"), DefaultPool).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("mergerfs: %v: %s", err, out)
	}
	return st, nil
}

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
func Assemble(ctx context.Context, tier string, branches []string, local int) error {
	mount := filepath.Join(PoolsDir, tier)
	if len(branches) == 0 {
		if mounted(mount) {
			_ = exec.CommandContext(ctx, "umount", "-l", mount).Run()
		}
		return nil
	}
	if err := os.MkdirAll(mount, 0o755); err != nil {
		return err
	}
	if mounted(mount) {
		if out, err := exec.CommandContext(ctx, "setfattr", "-n", "user.mergerfs.branches", "-v", strings.Join(branches, ":"), filepath.Join(mount, ".mergerfs")).CombinedOutput(); err != nil {
			return fmt.Errorf("update %s branches: %v: %s", tier, err, out)
		}
		return nil
	}
	create := "mfs"
	if local < len(branches) {
		create = "ff" // first branch with room: local before remote
	}
	opts := "allow_other,cache.files=partial,dropcacheonclose=true,category.create=" + create + ",moveonenospc=true,fsname=maitre-" + tier
	if out, err := exec.CommandContext(ctx, "mergerfs", "-o", opts, strings.Join(branches, ":"), mount).CombinedOutput(); err != nil {
		return fmt.Errorf("mergerfs %s: %v: %s", tier, err, out)
	}
	return nil
}
