// Package pool assembles the default Tier's Pool on one Node (ADR 0004: every
// Pool is MergerFS, even over one Drive). Milestone 2 knows one Drive: the
// data partition the Installer created, labelled maitre-data. If there is no
// Drive at all the Pool is a directory on the OS partition so Services still
// work; the Console warns about it.
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

// Up mounts every known Drive and assembles the default Pool over them.
// Idempotent: safe to run at every boot. extra are additional branch
// directories (Drives mounted by the storage manager, Milestone 4).
func Up(ctx context.Context, extra ...string) (*Status, error) {
	var branches []string
	if dev := "/dev/disk/by-label/" + DataLabel; exists(dev) {
		dir := filepath.Join(DrivesDir, "data")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if !mounted(dir) {
			if out, err := exec.CommandContext(ctx, "mount", "-o", "noatime", dev, dir).CombinedOutput(); err != nil {
				return nil, fmt.Errorf("mount %s: %v: %s", dev, err, out)
			}
		}
		branches = append(branches, dir)
	}
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
	opts := "cache.files=partial,dropcacheonclose=true,category.create=mfs,moveonenospc=true,fsname=maitre-default"
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
