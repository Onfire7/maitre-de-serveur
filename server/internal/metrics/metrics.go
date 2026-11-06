// Package metrics reads the Node gauges the Console shows (observability v1):
// CPU, memory, Pool capacity, network, temperature. Everything comes from
// /proc and /sys; nothing is stored.
package metrics

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Collector keeps the previous CPU and network counters for rates.
type Collector struct {
	mu sync.Mutex
	// PoolDir returns the mount of the Pool to report (the Space marked
	// default for new volumes), or "" when the Node has none: then
	// pool_fallback is set and capacity is zero.
	PoolDir func() string
	// Drives returns this Node's mounted Drives, drive id to mount path;
	// nil means none are reported.
	Drives func() map[string]string
	// ImageStoreDir returns podman's graphroot, which decision 319 moves
	// off the root partition; "" reports no image store gauge. Cached by
	// the caller: this runs on every reading.
	ImageStoreDir func() string
	lastAt        time.Time
	cpuBusy       uint64
	cpuTotal      uint64
	rx, tx        uint64
}

// New builds a Collector; poolDir resolves the Pool to report each time.
func New(poolDir func() string) *Collector {
	c := &Collector{PoolDir: poolDir}
	c.Read() // prime the counters
	return c
}

// Read takes a reading.
// SystemDiskDir is the path whose filesystem holds the OS, which is not
// always the one mounted at /.
//
// On a booted bootc Node / is a composefs image: the deployment's
// metadata, a few megabytes, and full by construction because nothing is
// ever written to it. Measured on Boulder, statfs("/") answered 8 MiB at
// 100% used while the partition underneath had 32 GiB — so the gauge meant
// to show a root filling up showed a permanent false alarm instead, and so
// did the greenboot check beside it. The partition is /sysroot, where
// ostree keeps its repo and /var.
//
// Which one to read is decided by asking rather than by detecting bootc:
// the larger of the two is the one with the space, which is also right on
// a plain Linux box, where /sysroot does not exist and answers zero.
//
// Exported because the Service manager reports the same filesystem when it
// moves the image store off it, and two copies of this rule would drift.
func SystemDiskDir() string { return systemDiskDir(disk) }

func systemDiskDir(stat func(string) (total, used uint64)) string {
	sysroot, _ := stat("/sysroot")
	root, _ := stat("/")
	if sysroot > root {
		return "/sysroot"
	}
	return "/"
}

func (c *Collector) Read() *maitrev1.NodeMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	m := &maitrev1.NodeMetrics{}

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
	if dir := c.PoolDir(); dir != "" {
		m.PoolTotalBytes, m.PoolUsedBytes = disk(dir)
	} else {
		m.PoolFallback = true
	}
	// The root filesystem: the gauge the Node never had, and the one that
	// says whether an update can still be staged.
	m.SystemDiskTotalBytes, m.SystemDiskUsedBytes = disk(systemDiskDir(disk))
	if c.ImageStoreDir != nil {
		if dir := c.ImageStoreDir(); dir != "" {
			m.ImageStoreDir = dir
			m.ImageStoreTotalBytes, m.ImageStoreUsedBytes = disk(dir)
		}
	}
	m.TemperatureC = temperature()
	m.Load_1M = load()
	if c.Drives != nil {
		for id, dir := range c.Drives() {
			if total, used := disk(dir); total > 0 {
				m.Drives = append(m.Drives, &maitrev1.DriveUsage{DriveId: id, TotalBytes: total, UsedBytes: used})
			}
		}
		sort.Slice(m.Drives, func(i, j int) bool { return m.Drives[i].GetDriveId() < m.Drives[j].GetDriveId() })
	}
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

// NetCounters is the Node's own network totals, which the agent reads
// around a pull to tell what it cost (decision 317): a pull runs in the
// Node's namespace, so there is nowhere else for those bytes to show.
func NetCounters() (rx, tx uint64) { return netCounters() }

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
