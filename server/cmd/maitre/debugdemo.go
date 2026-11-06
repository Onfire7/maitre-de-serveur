package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/debugconsole"
)

// debugDemo is `maitre debug --demo`: the Console with made-up data, to
// look at it without a Node, as `maitre tui --demo` does for the Node's
// own screen. The Node it invents is the one of 2026-11-04 — a root
// partition with a gigabyte left, an image store still on it, a Service
// restarting every five seconds and images nothing names — because that
// is the state every page here was designed to make legible.
type debugDemo struct{ started time.Time }

func (debugDemo) Info(context.Context) (*maitrev1.NodeInfo, error) {
	return &maitrev1.NodeInfo{
		NodeId: "9f2c41ab77de5510", Name: "avalon-1", FleetId: "Maison", Version: "0.9.3-debug",
		UptimeSeconds: 61 * 60 * 19, DebugBuild: true,
		Peers: []*maitrev1.PeerStatus{{NodeId: "boulder"}},
	}, nil
}

func (debugDemo) UpdateStatus(context.Context) (*maitrev1.UpdateStatus, error) {
	return &maitrev1.UpdateStatus{
		Booted:       &maitrev1.ImageDeployment{Image: "ghcr.io/onfire7/maitre-server:main", Version: "0.9.3-debug", Digest: "sha256:d9e853e81f2b4c7a"},
		Rollback:     &maitrev1.ImageDeployment{Image: "ghcr.io/onfire7/maitre-server:main", Version: "0.9.2"},
		State:        maitrev1.UpdateStatus_STATE_IDLE,
		Supported:    true,
		LastRollback: "greenboot rolled 0.9.4 back: the root filesystem had 412 MiB left",
	}, nil
}

func (debugDemo) Metrics(context.Context) (*maitrev1.NodeMetrics, error) {
	return &maitrev1.NodeMetrics{
		CpuPercent: 71.4, Load_1M: 4.12, TemperatureC: 61,
		MemoryTotalBytes: 16 << 30, MemoryUsedBytes: 11 << 30,
		PoolTotalBytes: 7 << 40, PoolUsedBytes: 3 << 40,
		SystemDiskTotalBytes: 24 << 30, SystemDiskUsedBytes: 23 << 30,
		ImageStoreTotalBytes: 24 << 30, ImageStoreUsedBytes: 23 << 30,
		ImageStoreDir:    "/var/lib/containers/storage",
		NetRxBytesPerSec: 94 << 20, NetTxBytesPerSec: 2 << 20,
	}, nil
}

func (debugDemo) Images(context.Context) (*maitrev1.ListImagesResponse, error) {
	return &maitrev1.ListImagesResponse{
		StoreDir: "/var/lib/containers/storage", FreeBytes: 1 << 30, TotalBytes: 24 << 30,
		Images: []*maitrev1.ListImagesResponse_Image{
			{Id: "7f3c1a9b2e4d8065", Reference: "jellyfin/jellyfin:10.9.11", SizeBytes: 3 << 30, Service: "jellyfin"},
			{Id: "b1d84e07cc935aa2", Reference: "jellyfin/jellyfin:10.9.10", SizeBytes: 3 << 30, Service: "jellyfin", RollbackTarget: true},
			{Id: "40ac5f61d9e8b773", Reference: "lscr.io/linuxserver/qbittorrent:5.0.3", SizeBytes: 1 << 30, Service: "qbittorrent"},
			{Id: "aa17c3e59b204d61", Reference: "lscr.io/linuxserver/qbittorrent:5.0.2", SizeBytes: 1 << 30},
			{Id: "ce920b4a7f135d88", Reference: "lscr.io/linuxserver/qbittorrent:5.0.1", SizeBytes: 1 << 30},
			{Id: "1b6f08da5c93e472", Reference: "louislam/uptime-kuma:1.23.13", SizeBytes: 512 << 20},
		},
	}, nil
}

func (debugDemo) Prune(_ context.Context, keepRollback bool) (*maitrev1.PruneImagesResponse, error) {
	kept := uint32(2)
	removed := []string{"lscr.io/linuxserver/qbittorrent:5.0.2", "lscr.io/linuxserver/qbittorrent:5.0.1", "louislam/uptime-kuma:1.23.13"}
	if !keepRollback {
		kept = 1
		removed = append(removed, "jellyfin/jellyfin:10.9.10")
	}
	return &maitrev1.PruneImagesResponse{
		StoreDir: "/var/lib/containers/storage", FreeBeforeBytes: 1 << 30, FreeAfterBytes: 4 << 30,
		Removed: removed, Kept: kept,
	}, nil
}

func (debugDemo) MoveStore(_ context.Context, dir string) (*maitrev1.MoveImageStoreResponse, error) {
	if dir == "" {
		dir = "/mnt/space-family/.maitre/imagestore"
	}
	return &maitrev1.MoveImageStoreResponse{
		From: "/var/lib/containers/storage", To: dir, MovedBytes: 9 << 30,
		RootFreeBeforeBytes: 1 << 30, RootFreeAfterBytes: 10 << 30,
		Services: []string{"jellyfin", "qbittorrent"},
	}, nil
}

func (debugDemo) Services(context.Context) ([]*maitrev1.ServiceStatus, error) {
	return []*maitrev1.ServiceStatus{
		{Spec: &maitrev1.ServiceSpec{Name: "jellyfin", Image: "jellyfin/jellyfin:10.9.11"}, State: maitrev1.ServiceStatus_STATE_RUNNING, Healthy: true},
		{Spec: &maitrev1.ServiceSpec{Name: "gluetun", Image: "qmcgaw/gluetun:v3.40"}, State: maitrev1.ServiceStatus_STATE_RUNNING, Healthy: true},
		{
			Spec: &maitrev1.ServiceSpec{Name: "qbittorrent", Image: "lscr.io/linuxserver/qbittorrent:5.0.3"},
			// Decision 315: the restart loop that re-pulled its image every
			// five seconds, and 317: its traffic is the Tunnel's.
			State: maitrev1.ServiceStatus_STATE_FAILED, Restarts: 1174,
			Message: "podman pull: writing blob: no space left on device", NetworkSharedWith: "gluetun",
		},
		{Spec: &maitrev1.ServiceSpec{Name: "uptime-kuma", Image: "louislam/uptime-kuma:1.23.13"}, State: maitrev1.ServiceStatus_STATE_STOPPED},
	}, nil
}

func (d debugDemo) Logs(_ context.Context, source string, lines int) (*maitrev1.GetNodeLogsResponse, error) {
	sources := []*maitrev1.LogSource{
		{Id: "agent", Label: "Maître agent"},
		{Id: "kernel", Label: "Kernel"},
		{Id: "service:qbittorrent", Label: "qbittorrent"},
	}
	var out []string
	t := d.started
	for i := 0; i < min(lines, 40); i++ {
		t = t.Add(5 * time.Second)
		stamp := t.Format("Jan 02 15:04:05")
		switch source {
		case "kernel":
			out = append(out, fmt.Sprintf("%s avalon-1 kernel: XFS (dm-0): Metadata I/O error: no space left on device", stamp))
		case "service:qbittorrent":
			out = append(out, fmt.Sprintf("%s avalon-1 qbittorrent[%d]: Error: writing blob: no space left on device", stamp, 4000+i))
		default:
			out = append(out, fmt.Sprintf("%s avalon-1 maitre[812]: level=WARN msg=\"pull failed\" service=qbittorrent err=\"no space left on device\" attempt=%d", stamp, 1174-40+i))
		}
	}
	return &maitrev1.GetNodeLogsResponse{Lines: out, Sources: sources}, nil
}

func (debugDemo) ReadFile(_ context.Context, path string) ([]byte, error) {
	switch path {
	case "/etc/containers/storage.conf":
		return []byte("[storage]\ndriver = \"overlay\"\nrunroot = \"/run/containers/storage\"\ngraphroot = \"/var/lib/containers/storage\"\n\n[storage.options]\nadditionalimagestores = [\"/usr/lib/containers/storage\"]\n\n[storage.options.overlay]\nmountopt = \"nodev,metacopy=on\"\n"), nil
	case "/var/lib/maitre/red-boots":
		return []byte("2\n"), nil
	case "/proc/mounts":
		return []byte("/dev/mapper/root / xfs rw,relatime,attr2,inode64,logbufs=8 0 0\n/dev/sda4 /var/lib/maitre ext4 rw,relatime 0 0\n"), nil
	}
	return nil, fmt.Errorf("%s is outside what a debug build will read (/etc/containers, /etc/systemd/system, /etc/greenboot, /var/log, /proc/mounts)", path)
}

func (debugDemo) Exec(_ context.Context, command string, args []string) (string, error) {
	line := strings.Join(append([]string{command}, args...), " ")
	switch {
	case strings.HasPrefix(line, "bootc status"):
		return "Current staged image: none\nCurrent booted image: ghcr.io/onfire7/maitre-server:main\n  Digest: sha256:d9e853e81f2b4c7a\n  Version: 0.9.3-debug\nCurrent rollback image: ghcr.io/onfire7/maitre-server:main\n  Version: 0.9.2\n", nil
	case strings.HasPrefix(line, "df"):
		return "Filesystem           Size  Used Avail Use% Mounted on\n/dev/mapper/root      24G   23G  1.0G  96% /\n/dev/sda4            2.0G  118M  1.7G   7% /var/lib/maitre\n", nil
	case strings.HasPrefix(line, "systemctl list-units --failed"):
		return "  UNIT                 LOAD   ACTIVE SUB    DESCRIPTION\n● qbittorrent.service  loaded failed failed qbittorrent\n\n1 loaded units listed.\n", nil
	}
	return "", fmt.Errorf("%s: this demo Node only answers bootc status, df and systemctl list-units --failed", command)
}

// demoDebugSource is the Source `maitre debug --demo` runs against.
func demoDebugSource() debugconsole.Source {
	return debugDemo{started: time.Now().Add(-200 * time.Second)}
}
