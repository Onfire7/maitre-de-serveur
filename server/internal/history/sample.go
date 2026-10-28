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
	prev map[string]metrics.UnitUsage
	at   time.Time
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
		}
	}
	s.prev, s.at = next, now
	return out
}
