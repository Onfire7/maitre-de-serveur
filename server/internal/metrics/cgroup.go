package metrics

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CgroupRoot is where the unified cgroup hierarchy is mounted.
var CgroupRoot = "/sys/fs/cgroup"

// UnitUsage is a systemd unit's counters from its cgroup (decision 210):
// the container a Quadlet unit runs lives inside it.
type UnitUsage struct {
	CPUMicros  uint64 // CPU time used, all cores
	Memory     uint64 // bytes in use, less reclaimable file cache
	ReadBytes  uint64
	WriteBytes uint64
	// Decision 317: the container's own network counters, read from the
	// network namespace its first process is in. OwnNetns is false when
	// that namespace is the Node's, which is what a Service on the Node's
	// own network has: the counters would then be the whole Node's and
	// mean nothing as the Service's own.
	RxBytes  uint64
	TxBytes  uint64
	OwnNetns bool
}

// Unit reads a system unit's counters; false when it is not running.
func Unit(unit string) (UnitUsage, bool) {
	dir := filepath.Join(CgroupRoot, "system.slice", unit)
	var u UnitUsage
	cpu, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return u, false
	}
	u.CPUMicros = field(string(cpu), "usage_usec")
	if b, err := os.ReadFile(filepath.Join(dir, "memory.current")); err == nil {
		u.Memory, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if st, err := os.ReadFile(filepath.Join(dir, "memory.stat")); err == nil {
			if inactive := field(string(st), "inactive_file"); inactive < u.Memory {
				u.Memory -= inactive
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "io.stat")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			for _, kv := range strings.Fields(line) {
				k, v, _ := strings.Cut(kv, "=")
				n, _ := strconv.ParseUint(v, 10, 64)
				switch k {
				case "rbytes":
					u.ReadBytes += n
				case "wbytes":
					u.WriteBytes += n
				}
			}
		}
	}
	u.RxBytes, u.TxBytes, u.OwnNetns = unitNetwork(dir)
	return u, true
}

// unitNetwork reads a unit's network counters from inside its container,
// without asking podman: the unit's cgroup holds the container's
// processes, and a process's /proc entry carries both its namespace and
// its namespace's counters. A pull runs in the Node's own namespace, which
// is exactly why these numbers read zero during the failure that started
// all this and why the agent counts pull bytes itself.
func unitNetwork(dir string) (rx, tx uint64, own bool) {
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return 0, 0, false
	}
	pid, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if pid == "" {
		return 0, 0, false
	}
	// The Node's own namespace means the Service has none of its own.
	host, err1 := os.Readlink("/proc/1/ns/net")
	theirs, err2 := os.Readlink(filepath.Join("/proc", pid, "ns", "net"))
	if err1 != nil || err2 != nil || theirs == host {
		return 0, 0, false
	}
	dev, err := os.ReadFile(filepath.Join("/proc", pid, "net", "dev"))
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(dev), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" || name == "lo" || name == "Inter-" || name == "face" {
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
	return rx, tx, true
}

// field reads "key value" from a cgroup stat file.
func field(body, key string) uint64 {
	for _, line := range strings.Split(body, "\n") {
		if k, v, ok := strings.Cut(line, " "); ok && k == key {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}
