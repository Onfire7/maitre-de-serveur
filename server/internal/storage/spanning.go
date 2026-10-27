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
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Spanning Spaces (decisions 105, 106, 127, 188): every Node exports, per
// Space, a Pool over its own Drives only (pool.SharedDir; the full Pool holds
// the peers' Pools, which hold ours, and listing it looped between Nodes)
// over NFS with mutual TLS (nfstls.go) to the Fleet's other Nodes only, and
// mounts theirs soft, so a Node that is off makes its files absent rather
// than hanging anything. A spanning Space's Pool on a Node is mergerfs over
// its local Space folders, then the peers' shared Pools; the Space's
// placement decides where new files land.

// RemoteDir is where peers' Pools are mounted: RemoteDir/<node_id>/<space>;
// a variable for tests.
var RemoteDir = "/var/mnt/remote"

// ExportsFile is the NFS export list this package owns; a variable for tests.
var ExportsFile = "/etc/exports.d/maitre.exports"

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

// WriteExports exports every mounted Pool to the given hosts and makes sure
// the NFS server runs while anything is exported. No hosts means nothing is
// exported.
func WriteExports(ctx context.Context, run Runner, spaces []string, hosts []string) error {
	var b strings.Builder
	b.WriteString("# Written by maitre: this Node's Pools, for the Fleet's other Nodes only.\n")
	sort.Strings(spaces)
	exported := 0
	for i, space := range spaces {
		mount := filepath.Join(pool.SharedDir, space)
		if !mounted(mount) || len(hosts) == 0 {
			continue
		}
		if exported == 0 {
			// The NFSv4 root, explicitly: left to itself mountd builds one
			// from "/", which on a bootc Node is composefs and cannot be
			// exported ("Cannot export /"), so every path below it answered
			// "No such file or directory" (2026-10-27). Read-only, and holds
			// nothing but the shared Pools' mount points; peers mount
			// <name>:/<space>.
			b.WriteString(pool.SharedDir)
			for _, h := range hosts {
				fmt.Fprintf(&b, " %s(ro,sync,no_subtree_check,root_squash,xprtsec=mtls,fsid=0)", h)
			}
			b.WriteString("\n")
		}
		exported++
		b.WriteString(mount)
		for _, h := range hosts {
			fmt.Fprintf(&b, " %s(rw,sync,no_subtree_check,no_root_squash,xprtsec=mtls,fsid=%d)", h, 1000+i)
		}
		b.WriteString("\n")
	}
	if err := os.MkdirAll(filepath.Dir(ExportsFile), 0o755); err != nil {
		return err
	}
	cur, _ := os.ReadFile(ExportsFile)
	changed := string(cur) != b.String()
	if changed {
		if err := os.WriteFile(ExportsFile, []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	if exported == 0 && !changed {
		return nil
	}
	// The image does not enable the NFS server; the agent starts it, on
	// every call and not only when the list changed: after a reboot (every
	// update) the list is unchanged, and nothing else would start it, so
	// peers could not mount this Node's Pools (2026-10-27).
	if _, err := run.Run(ctx, "systemctl", "start", "--no-block", "nfs-server.service"); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	_, err := run.Run(ctx, "exportfs", "-ra")
	return err
}

// RemoteBranches mounts, for each spanning Space, the Pools of peers that
// hold Drives of that Space, and returns the mount points by Space. Peers
// without an address, or whose mount fails, are skipped and listed in
// missing ("<node> <space>: why"), so the caller can try again later.
func (m *Manager) RemoteBranches(ctx context.Context, run Runner, peerAddr map[string]string) (out map[string][]string, missing []string, err error) {
	spaces, err := Spaces(ctx, m.st)
	if err != nil {
		return nil, nil, err
	}
	spanning := map[string]bool{}
	for _, b := range spaces {
		if b.GetSpanNodes() {
			spanning[b.GetSpaceId()] = true
		}
	}
	records, err := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, nil, err
	}
	// Which peers hold Drives of which spanning Space.
	holders := map[string]map[string]bool{}
	for _, r := range records {
		if r.GetNodeId() == m.st.NodeID() {
			continue
		}
		for _, id := range SpacesOf(r, spaces) {
			if !spanning[id] {
				continue
			}
			if holders[id] == nil {
				holders[id] = map[string]bool{}
			}
			holders[id][r.GetNodeId()] = true
		}
	}
	out = map[string][]string{}
	for tier, nodes := range holders {
		for nodeID := range nodes {
			if _, ok := peerAddr[nodeID]; !ok {
				missing = append(missing, fmt.Sprintf("%s %s: no address", nodeID, tier))
				continue
			}
			if !HasNFSCertificate(ctx, m.st, nodeID) {
				missing = append(missing, fmt.Sprintf("%s %s: runs a Maître Server from before NFS over TLS; update it", nodeID, tier))
				continue
			}
			dir, err := MountRemotePool(ctx, run, nodeID, tier)
			if err != nil {
				missing = append(missing, fmt.Sprintf("%s %s: %v", nodeID, tier, err))
				continue
			}
			out[tier] = append(out[tier], dir)
		}
	}
	for tier := range out {
		sort.Strings(out[tier])
	}
	sort.Strings(missing)
	return out, missing, nil
}

// MountRemotePool mounts a peer's Pool of one Space (exported to this Node)
// at RemoteDir/<node>/<space>, over mutual TLS, soft with short timeouts,
// and returns the directory; already mounted is fine. The peer is reached by
// its NFS name, which WriteNFSPeers maps to its address.
func MountRemotePool(ctx context.Context, run Runner, nodeID, space string) (string, error) {
	dir := filepath.Join(RemoteDir, nodeID, space)
	if mounted(dir) {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Relative to the peer's NFSv4 root, its SharedDir (WriteExports).
	src := fmt.Sprintf("%s:/%s", identity.NFSName(nodeID), space)
	if _, err := run.Run(ctx, "timeout", "20", "mount", "-t", "nfs4", "-o", "soft,timeo=30,retrans=2,noatime,_netdev,xprtsec=mtls", src, dir); err != nil {
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
