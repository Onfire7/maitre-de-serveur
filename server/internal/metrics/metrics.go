// Package metrics reads the Node gauges the Console shows (observability v1):
// CPU, memory, Pool capacity, network, temperature. Everything comes from
// /proc and /sys; nothing is stored.
package metrics

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Collector keeps the previous CPU and network counters for rates.
type Collector struct {
	mu       sync.Mutex
	poolDir  string
	fallback bool
	lastAt   time.Time
	cpuBusy  uint64
	cpuTotal uint64
	rx, tx   uint64
}

// New builds a Collector for the Pool at poolDir.
func New(poolDir string, fallback bool) *Collector {
	c := &Collector{poolDir: poolDir, fallback: fallback}
	c.Read() // prime the counters
	return c
}

// Read takes a reading.
func (c *Collector) Read() *maitrev1.NodeMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	m := &maitrev1.NodeMetrics{PoolFallback: c.fallback}

	busy, total := cpuCounters()
	if c.cpuTotal > 0 && total > c.cpuTotal {
		m.CpuPercent = 100 * float64(busy-c.cpuBusy) / float64(total-c.cpuTotal)
	}
	c.cpuBusy, c.cpuTotal = busy, total

	rx, tx := netCounters()
	if dt := now.Sub(c.lastAt).Seconds(); !c.lastAt.IsZero() && dt > 0 {
		m.NetRxBytesPerSec = float64(rx-c.rx) / dt
		m.NetTxBytesPerSec = float64(tx-c.tx) / dt
	}
	c.rx, c.tx, c.lastAt = rx, tx, now

	m.MemoryTotalBytes, m.MemoryUsedBytes = memory()
	m.PoolTotalBytes, m.PoolUsedBytes = disk(c.poolDir)
	m.TemperatureC = temperature()
	m.Load_1M = load()
	return m
}

func cpuCounters() (busy, total uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0
	}
	var vals []uint64
	for _, s := range f[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		vals = append(vals, v)
	}
	for i, v := range vals {
		total += v
		if i != 3 && i != 4 { // idle, iowait
			busy += v
		}
	}
	return busy, total
}

func memory() (total, used uint64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var avail uint64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	if total > avail {
		used = total - avail
	}
	return total, used
}

func disk(dir string) (total, used uint64) {
	var st syscall.Statfs_t
	if dir == "" || syscall.Statfs(dir, &st) != nil {
		return 0, 0
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	if total > free {
		used = total - free
	}
	return total, used
}

func netCounters() (rx, tx uint64) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "podman") || strings.HasPrefix(name, "cni") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

func temperature() float64 {
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	var max float64
	for _, z := range zones {
		b, err := os.ReadFile(z)
		if err != nil {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err == nil && v/1000 > max && v/1000 < 150 {
			max = v / 1000
		}
	}
	return max
}

func load() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
