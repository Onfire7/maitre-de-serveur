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

// Spanning Tiers (decisions 105, 106): every Node exports its Pools over
// NFS to the Fleet's other Nodes only, and mounts theirs soft, so a Node
// that is off makes its files absent rather than hanging anything. A
// Spanning Tier's Pool on a Node is mergerfs over its local Drives first,
// then the remote Pools: new files land locally while there is room.

// RemoteDir is where peers' Pools are mounted: RemoteDir/<node_id>/<tier>.
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
func WriteExports(ctx context.Context, run Runner, tiers []string, hosts []string) error {
	var b strings.Builder
	b.WriteString("# Written by maitre: this Node's Pools, for the Fleet's other Nodes only.\n")
	for i, tier := range tiers {
		mount := filepath.Join(pool.PoolsDir, tier)
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

// RemoteBranches mounts, for each Spanning Tier, the Pools of peers that
// hold Drives of that Tier, and returns the mount points by Tier. Peers
// without a reachable address, or whose mount fails, are skipped.
func (m *Manager) RemoteBranches(ctx context.Context, run Runner, peerAddr map[string]string) (map[string][]string, error) {
	tiers, err := Tiers(ctx, m.st)
	if err != nil {
		return nil, err
	}
	records, err := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, err
	}
	// Which peers hold Drives of which Spanning Tier.
	holders := map[string]map[string]bool{}
	for _, r := range records {
		if r.GetNodeId() == m.st.NodeID() {
			continue
		}
		tier := TierOf(r, tiers)
		for _, t := range tiers {
			if t.GetTierId() == tier && t.GetPlacement() == "spanning" {
				if holders[tier] == nil {
					holders[tier] = map[string]bool{}
				}
				holders[tier][r.GetNodeId()] = true
			}
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
			dir := filepath.Join(RemoteDir, nodeID, tier)
			if !mounted(dir) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					continue
				}
				src := fmt.Sprintf("%s:%s", host, filepath.Join(pool.PoolsDir, tier))
				if _, err := run.Run(ctx, "timeout", "20", "mount", "-t", "nfs4", "-o", "soft,timeo=30,retrans=2,noatime,_netdev", src, dir); err != nil {
					continue
				}
			}
			out[tier] = append(out[tier], dir)
		}
	}
	for tier := range out {
		sort.Strings(out[tier])
	}
	return out, nil
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
