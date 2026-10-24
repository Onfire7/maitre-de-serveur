package metrics_test

import (
	"testing"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/metrics"
)

func TestReadsSomething(t *testing.T) {
	c := metrics.New(t.TempDir(), true)
	time.Sleep(50 * time.Millisecond)
	m := c.Read()
	if m.GetMemoryTotalBytes() == 0 || m.GetPoolTotalBytes() == 0 || !m.GetPoolFallback() {
		t.Fatalf("unexpected reading %+v", m)
	}
	if m.GetCpuPercent() < 0 || m.GetCpuPercent() > 100 {
		t.Fatalf("cpu %v", m.GetCpuPercent())
	}
}
