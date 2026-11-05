package history

import (
	"runtime"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/metrics"
)

func TestSamplerRates(t *testing.T) {
	use := metrics.UnitUsage{CPUMicros: 1_000_000, Memory: 500, ReadBytes: 100, WriteBytes: 0}
	s := &Sampler{
		Node: func() *maitrev1.NodeMetrics {
			return &maitrev1.NodeMetrics{CpuPercent: 12, MemoryTotalBytes: 200, MemoryUsedBytes: 50, PoolFallback: true, Drives: []*maitrev1.DriveUsage{{DriveId: "sda", TotalBytes: 4, UsedBytes: 1}}}
		},
		Services: func() []string { return []string{"jellyfin", "stopped"} },
		Unit: func(u string) (metrics.UnitUsage, bool) {
			return use, u == "jellyfin.service"
		},
	}
	r := s.Read(t0)
	if r["node/cpu"] != 12 || r["node/memory"] != 25 || r["drive/sda"] != 25 || r["service/jellyfin/memory"] != 500 {
		t.Fatalf("%v", r)
	}
	for _, k := range []string{"node/pool", "node/temperature", "service/jellyfin/cpu", "service/stopped/memory"} {
		if _, ok := r[k]; ok {
			t.Fatalf("%s should be a gap: %v", k, r)
		}
	}
	use.CPUMicros += 10_000_000 * uint64(runtime.NumCPU()) / 2
	use.ReadBytes += 1000
	r = s.Read(t0.Add(10 * time.Second))
	if r["service/jellyfin/cpu"] != 50 || r["service/jellyfin/disk_read"] != 100 || r["service/jellyfin/disk_write"] != 0 {
		t.Fatalf("%v", r)
	}
	// Restarted: counters went back, so no rates this time.
	use = metrics.UnitUsage{CPUMicros: 5, Memory: 1}
	r = s.Read(t0.Add(20 * time.Second))
	if _, ok := r["service/jellyfin/cpu"]; ok || r["service/jellyfin/memory"] != 1 {
		t.Fatalf("%v", r)
	}
}

// Decision 317 and the root gauge: the Node had no series for the
// filesystem that actually filled, and a Service's network has to be
// attributed to whoever owns the namespace it is in.
func TestTheFilesystemsAndTheNetworkAreSampled(t *testing.T) {
	tunnel := metrics.UnitUsage{CPUMicros: 1, Memory: 1, RxBytes: 1000, TxBytes: 100, OwnNetns: true}
	member := metrics.UnitUsage{CPUMicros: 1, Memory: 1, RxBytes: 1000, TxBytes: 100, OwnNetns: true}
	onHost := metrics.UnitUsage{CPUMicros: 1, Memory: 1, RxBytes: 9e9, TxBytes: 9e9, OwnNetns: false}
	units := map[string]*metrics.UnitUsage{"gluetun.service": &tunnel, "qbittorrent.service": &member, "pihole.service": &onHost}
	pulled := uint64(0)
	s := &Sampler{
		Node: func() *maitrev1.NodeMetrics {
			return &maitrev1.NodeMetrics{
				SystemDiskTotalBytes: 100, SystemDiskUsedBytes: 97,
				ImageStoreTotalBytes: 1000, ImageStoreUsedBytes: 250,
			}
		},
		Services: func() []string { return []string{"gluetun", "qbittorrent", "pihole"} },
		Unit: func(u string) (metrics.UnitUsage, bool) {
			if usage, ok := units[u]; ok {
				return *usage, true
			}
			return metrics.UnitUsage{}, false
		},
		// qbittorrent is inside gluetun's namespace; pihole is on the
		// Node's own network, so its traffic is the Node's.
		NetOwner: func(name string) string {
			switch name {
			case "qbittorrent":
				return "gluetun"
			case "pihole":
				return ""
			}
			return name
		},
		Pulls: func() map[string]uint64 { return map[string]uint64{"gluetun": pulled} },
	}
	r := s.Read(t0)
	if r["node/system_disk"] != 97 {
		t.Fatalf("the root filesystem is the gauge this all started for: %v", r["node/system_disk"])
	}
	if r["node/image_store"] != 25 {
		t.Fatalf("image store: %v", r["node/image_store"])
	}

	tunnel.RxBytes += 2000
	member.RxBytes += 2000
	onHost.RxBytes += 9e9
	pulled += 5000
	r = s.Read(t0.Add(10 * time.Second))
	if r["service/gluetun/net_rx"] != 200 {
		t.Fatalf("the Tunnel reports its own namespace: %v", r)
	}
	if _, ok := r["service/qbittorrent/net_rx"]; ok {
		t.Fatalf("a member would count the Tunnel's bytes a second time: %v", r)
	}
	if _, ok := r["service/pihole/net_rx"]; ok {
		t.Fatalf("a Service on the Node's own network has no traffic of its own: %v", r)
	}
	if r["service/gluetun/image_pull"] != 500 {
		t.Fatalf("pull bytes are the agent's own tally: %v", r)
	}
}

// A Service that cannot start is exactly the one that pulls without end,
// so its pull series must not depend on a running unit.
func TestPullBytesAreReportedForAStoppedService(t *testing.T) {
	pulled := uint64(0)
	s := &Sampler{
		Services: func() []string { return []string{"ollama"} },
		Unit:     func(string) (metrics.UnitUsage, bool) { return metrics.UnitUsage{}, false },
		Pulls:    func() map[string]uint64 { return map[string]uint64{"ollama": pulled} },
	}
	s.Read(t0)
	pulled += 470_000_000
	r := s.Read(t0.Add(10 * time.Second))
	if r["service/ollama/image_pull"] != 47_000_000 {
		t.Fatalf("a stopped Service's pulls must still show: %v", r)
	}
	// The agent restarting takes the counter back to zero: a gap, not a
	// spike downwards.
	pulled = 0
	r = s.Read(t0.Add(20 * time.Second))
	if _, ok := r["service/ollama/image_pull"]; ok {
		t.Fatalf("a reset counter should leave a gap: %v", r)
	}
}
