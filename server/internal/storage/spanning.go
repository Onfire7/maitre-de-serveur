package storage

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Spanning Bins (decisions 105, 106, 127): every Node exports its Pools over
// NFS to the Fleet's other Nodes only, and mounts theirs soft, so a Node
// that is off makes its files absent rather than hanging anything. A
// spanning Bin's Pool on a Node is mergerfs over its local Bin folders, then
// the remote Pools; the Bin's placement decides where new files land.

// RemoteDir is where peers' Pools are mounted: RemoteDir/<node_id>/<bin>.
const RemoteDir = "/var/mnt/remote"

// ExportsFile is the NFS export list this package owns.
const ExportsFile = "/etc/exports.d/maitre.exports"

// PeerHosts returns the IP addresses (no ports) of peers, from their
// addresses as the sync layer knows them.
func PeerHosts(peers []*maitrev1.PeerStatus, extra map[string][]string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(a string) {
		h, _, err := net.SplitHostPort(a)
		if err != nil {
			h = a
		}
		if ip := net.ParseIP(h); ip != nil && !seen[ip.String()] {
			seen[ip.String()] = true
			out = append(out, ip.String())
		}
	}
	for _, p := range peers {
		add(p.GetAddress())
		for _, a := range extra[p.GetNodeId()] {
			add(a)
		}
	}
	sort.Strings(out)
	return out
}

// WriteExports exports every mounted Pool to the given hosts and reloads
// the NFS server. No hosts means nothing is exported.
func WriteExports(ctx context.Context, run Runner, bins []string, hosts []string) error {
	var b strings.Builder
	b.WriteString("# Written by maitre: this Node's Pools, for the Fleet's other Nodes only.\n")
	sort.Strings(bins)
	for i, bin := range bins {
		mount := filepath.Join(pool.PoolsDir, bin)
		if !mounted(mount) || len(hosts) == 0 {
			continue
		}
		b.WriteString(mount)
		for _, h := range hosts {
			fmt.Fprintf(&b, " %s(rw,sync,no_subtree_check,no_root_squash,fsid=%d)", h, 1000+i)
		}
		b.WriteString("\n")
	}
	if err := os.MkdirAll(filepath.Dir(ExportsFile), 0o755); err != nil {
		return err
	}
	cur, _ := os.ReadFile(ExportsFile)
	if string(cur) == b.String() {
		return nil
	}
	if err := os.WriteFile(ExportsFile, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if _, err := run.Run(ctx, "systemctl", "start", "--no-block", "nfs-server.service"); err != nil {
		return err
	}
	_, err := run.Run(ctx, "exportfs", "-ra")
	return err
}

// RemoteBranches mounts, for each spanning Bin, the Pools of peers that
// hold Drives of that Bin, and returns the mount points by Bin. Peers
// without a reachable address, or whose mount fails, are skipped.
func (m *Manager) RemoteBranches(ctx context.Context, run Runner, peerAddr map[string]string) (map[string][]string, error) {
	bins, err := Bins(ctx, m.st)
	if err != nil {
		return nil, err
	}
	spanning := map[string]bool{}
	for _, b := range bins {
		if b.GetSpanNodes() {
			spanning[b.GetBinId()] = true
		}
	}
	records, err := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, err
	}
	// Which peers hold Drives of which spanning Bin.
	holders := map[string]map[string]bool{}
	for _, r := range records {
		if r.GetNodeId() == m.st.NodeID() {
			continue
		}
		for _, id := range BinsOf(r, bins) {
			if !spanning[id] {
				continue
			}
			if holders[id] == nil {
				holders[id] = map[string]bool{}
			}
			holders[id][r.GetNodeId()] = true
		}
	}
	out := map[string][]string{}
	for tier, nodes := range holders {
		for nodeID := range nodes {
			host, ok := peerAddr[nodeID]
			if !ok {
				continue
			}
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			dir, err := MountRemotePool(ctx, run, host, nodeID, tier)
			if err != nil {
				continue
			}
			out[tier] = append(out[tier], dir)
		}
	}
	for tier := range out {
		sort.Strings(out[tier])
	}
	return out, nil
}

// MountRemotePool mounts a peer's Pool of one Bin (exported to this Node)
// at RemoteDir/<node>/<bin>, soft with short timeouts, and returns the
// directory; already mounted is fine. host is the peer's IP.
func MountRemotePool(ctx context.Context, run Runner, host, nodeID, bin string) (string, error) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	dir := filepath.Join(RemoteDir, nodeID, bin)
	if mounted(dir) {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src := fmt.Sprintf("%s:%s", host, filepath.Join(pool.PoolsDir, bin))
	if _, err := run.Run(ctx, "timeout", "20", "mount", "-t", "nfs4", "-o", "soft,timeo=30,retrans=2,noatime,_netdev", src, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// UnmountStale lazily unmounts remote Pools of peers no longer in the Fleet.
func UnmountStale(ctx context.Context, run Runner, peers map[string]bool) {
	entries, _ := os.ReadDir(RemoteDir)
	for _, e := range entries {
		if !e.IsDir() || peers[e.Name()] {
			continue
		}
		tiers, _ := os.ReadDir(filepath.Join(RemoteDir, e.Name()))
		for _, t := range tiers {
			dir := filepath.Join(RemoteDir, e.Name(), t.Name())
			if mounted(dir) {
				_, _ = run.Run(ctx, "umount", "-l", dir)
			}
		}
	}
}
