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
	return u, true
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
