package history

import (
	"runtime"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/metrics"
)

// Every is how often a Node takes its readings.
const Every = 10 * time.Second

// Sampler turns the Node's gauges and its Services' cgroup counters into
// one reading per series.
type Sampler struct {
	// Node reads the Node's gauges; it needs a metrics.Collector of its own,
	// since its rates are since its previous call.
	Node func() *maitrev1.NodeMetrics
	// Services lists the Services this Node runs, by name.
	Services func() []string
	// Unit reads a unit's counters; metrics.Unit when nil.
	Unit func(unit string) (metrics.UnitUsage, bool)
	// NetOwner names whose network namespace carries a Service's traffic
	// (decision 317): the Service itself when it has its own, the Tunnel
	// it joins when it shares one, and "" when it is on the Node's own
	// network and the traffic is the Node's. Nil treats every Service as
	// its own, which is what the counters already say.
	NetOwner func(name string) string
	// Pulls is bytes each Service's images have cost to pull since the
	// agent started, which only the agent can know: a pull runs in the
	// Node's own network namespace, so the per-Service counters read zero
	// through exactly the failure that made this series necessary.
	Pulls    func() map[string]uint64
	prev     map[string]metrics.UnitUsage
	prevPull map[string]uint64
	at       time.Time
}

// Read takes the readings at now.
func (s *Sampler) Read(now time.Time) map[string]float64 {
	out := map[string]float64{}
	if s.Node != nil {
		m := s.Node()
		out["node/cpu"] = m.GetCpuPercent()
		if t := m.GetMemoryTotalBytes(); t > 0 {
			out["node/memory"] = 100 * float64(m.GetMemoryUsedBytes()) / float64(t)
		}
		out["node/load"] = m.GetLoad_1M()
		out["node/net_rx"] = m.GetNetRxBytesPerSec()
		out["node/net_tx"] = m.GetNetTxBytesPerSec()
		// No sensor reads as 0: leave a gap rather than a line at zero.
		if c := m.GetTemperatureC(); c > 0 {
			out["node/temperature"] = c
		}
		if t := m.GetPoolTotalBytes(); t > 0 && !m.GetPoolFallback() {
			out["node/pool"] = 100 * float64(m.GetPoolUsedBytes()) / float64(t)
		}
		if t := m.GetSystemDiskTotalBytes(); t > 0 {
			out["node/system_disk"] = 100 * float64(m.GetSystemDiskUsedBytes()) / float64(t)
		}
		if t := m.GetImageStoreTotalBytes(); t > 0 {
			out["node/image_store"] = 100 * float64(m.GetImageStoreUsedBytes()) / float64(t)
		}
		for _, d := range m.GetDrives() {
			if d.GetTotalBytes() > 0 {
				out["drive/"+d.GetDriveId()] = 100 * float64(d.GetUsedBytes()) / float64(d.GetTotalBytes())
			}
		}
	}
	unit := s.Unit
	if unit == nil {
		unit = metrics.Unit
	}
	dt := now.Sub(s.at).Seconds()
	next := map[string]metrics.UnitUsage{}
	if s.Services != nil {
		for _, name := range s.Services() {
			u, ok := unit(name + ".service")
			if !ok {
				continue // stopped: a gap
			}
			next[name] = u
			p := "service/" + name + "/"
			out[p+"memory"] = float64(u.Memory)
			// Rates need the previous reading, of the same run of the unit
			// (counters start over when it restarts).
			was, ok := s.prev[name]
			if !ok || dt <= 0 || u.CPUMicros < was.CPUMicros || u.ReadBytes < was.ReadBytes || u.WriteBytes < was.WriteBytes {
				continue
			}
			out[p+"cpu"] = 100 * float64(u.CPUMicros-was.CPUMicros) / (dt * 1e6 * float64(runtime.NumCPU()))
			out[p+"disk_read"] = float64(u.ReadBytes-was.ReadBytes) / dt
			out[p+"disk_write"] = float64(u.WriteBytes-was.WriteBytes) / dt
			// Decision 317: only the namespace's owner reports its traffic.
			// A member of a Tunnel would otherwise report the Tunnel's
			// bytes under its own name, and a sum over Services would
			// count them once per member.
			owner := name
			if s.NetOwner != nil {
				owner = s.NetOwner(name)
			}
			if u.OwnNetns && owner == name && u.RxBytes >= was.RxBytes && u.TxBytes >= was.TxBytes {
				out[p+"net_rx"] = float64(u.RxBytes-was.RxBytes) / dt
				out[p+"net_tx"] = float64(u.TxBytes-was.TxBytes) / dt
			}
		}
	}
	// Pull bytes stand apart from the cgroup counters: they are the agent's
	// own tally, and they are worth reporting for a Service whose unit is
	// not running, since a Service that cannot start is precisely the one
	// that pulls without end.
	pulls := map[string]uint64{}
	if s.Pulls != nil {
		pulls = s.Pulls()
		for name, total := range pulls {
			was, ok := s.prevPull[name]
			if !ok || dt <= 0 || total < was {
				continue // the agent restarted: a gap, not a spike
			}
			out["service/"+name+"/image_pull"] = float64(total-was) / dt
		}
	}
	s.prev, s.prevPull, s.at = next, pulls, now
	return out
}
