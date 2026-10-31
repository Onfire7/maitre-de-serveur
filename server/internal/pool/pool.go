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
// Placement is how a Space fills its Drives (decisions 201, 202): Fill is
// most_free, most_free_percent, least_free or least_free_percent; with
// PreferLocal, this Node's own Drives fill first (in that order) and peers'
// Pools only once they are full.
type Placement struct {
	Fill        string
	PreferLocal bool
}

// PlacementOf reads a Space's placement. The old "local_first" is most
// free space with local Drives preferred, which is what it did.
func PlacementOf(placement string, preferLocal bool) Placement {
	switch placement {
	case "local_first":
		return Placement{Fill: "most_free", PreferLocal: true}
	case "most_free", "most_free_percent", "least_free", "least_free_percent":
		return Placement{Fill: placement, PreferLocal: preferLocal}
	default:
		return Placement{Fill: "most_free", PreferLocal: preferLocal}
	}
}

// Assemble mounts (or re-shapes in place) the Pool of one Space over its
// branches: this Node's Space folders first (local of them), then peers'
// Pools. Idempotent; an empty branch list unmounts. Branches in noCreate
// take no new files (mergerfs "=NC", decision 247): a draining Drive's
// files stay visible while they move.
func Assemble(ctx context.Context, space string, branches []string, local int, p Placement, noCreate map[string]bool) error {
	return assemble(ctx, filepath.Join(PoolsDir, space), "maitre-"+space, branches, local, p, noCreate)
}

// AssembleShared mounts the local-only Pool of one Space at
// SharedDir/<space>, over this Node's own branches.
func AssembleShared(ctx context.Context, space string, branches []string, p Placement, noCreate map[string]bool) error {
	return assemble(ctx, filepath.Join(SharedDir, space), "maitre-shared-"+space, branches, len(branches), p, noCreate)
}

// Branches writes the branch list as mergerfs takes it, a no-create branch
// as "<path>=NC".
func Branches(branches []string, noCreate map[string]bool) string {
	out := make([]string, len(branches))
	for i, b := range branches {
		out[i] = b
		if noCreate[b] {
			out[i] += "=NC"
		}
	}
	return strings.Join(out, ":")
}

func assemble(ctx context.Context, mount, fsname string, branches []string, local int, p Placement, noCreate map[string]bool) error {
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
	branches, create := Plan(p, branches, local, statBranch)
	if mounted(mount) {
		ctl := filepath.Join(mount, ".mergerfs")
		if out, err := exec.CommandContext(ctx, "setfattr", "-n", "user.mergerfs.branches", "-v", Branches(branches, noCreate), ctl).CombinedOutput(); err != nil {
			return fmt.Errorf("update %s branches: %v: %s", space, err, out)
		}
		_ = exec.CommandContext(ctx, "setfattr", "-n", "user.mergerfs.category.create", "-v", create, ctl).Run()
		return nil
	}
	opts := "allow_other,cache.files=partial,dropcacheonclose=true,category.create=" + create + ",moveonenospc=true,fsname=" + fsname
	if out, err := exec.CommandContext(ctx, "mergerfs", "-o", opts, Branches(branches, noCreate), mount).CombinedOutput(); err != nil {
		return fmt.Errorf("mergerfs %s: %v: %s", space, err, out)
	}
	return nil
}

// Stat is a branch's free bytes and share of free space; ok is false when
// it cannot be read.
type Stat func(dir string) (free uint64, share float64, ok bool)

// Plan returns the branch order and mergerfs create policy for a
// placement. The first local branches are this Node's own Drives.
//
// Preferring local Drives with peers present: the local branches ordered
// by the fill, then the peers', and "ff" (the first with room), so peers
// get new files only once every local Drive is full. Otherwise mergerfs's
// own policy for the fill over every branch: mfs, pfrd, lfs; it has none
// for the least free share, which is the branches ordered fullest first
// and "ff". An order is refreshed whenever the Pool is assembled (and
// every ten minutes), so "most free" by order fills one Drive for a while
// before moving on rather than balancing each file.
func Plan(p Placement, branches []string, local int, stat Stat) ([]string, string) {
	if local > len(branches) {
		local = len(branches)
	}
	if p.PreferLocal && local < len(branches) {
		return append(Order(p.Fill, branches[:local], stat), branches[local:]...), "ff"
	}
	switch p.Fill {
	case "most_free_percent":
		return branches, "pfrd"
	case "least_free":
		return branches, "lfs"
	case "least_free_percent":
		return Order(p.Fill, branches, stat), "ff"
	default:
		return branches, "mfs"
	}
}

// Order sorts branches for a fill, the one to take first first: most free
// bytes or share, or least. A branch that cannot be read goes last; equal
// ones keep their order.
func Order(fill string, branches []string, stat Stat) []string {
	out := append([]string(nil), branches...)
	type key struct {
		ok    bool
		value float64
	}
	keys := map[string]key{}
	for _, b := range out {
		free, share, ok := stat(b)
		v := float64(free)
		if fill == "most_free_percent" || fill == "least_free_percent" {
			v = share
		}
		if fill == "most_free" || fill == "most_free_percent" {
			v = -v
		}
		keys[b] = key{ok, v}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := keys[out[i]], keys[out[j]]
		if a.ok != b.ok {
			return a.ok
		}
		return a.value < b.value
	})
	return out
}

// Mounted reports whether dir is a mount point.
func Mounted(dir string) bool { return mounted(dir) }
