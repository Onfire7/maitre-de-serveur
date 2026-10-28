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
