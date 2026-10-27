package metrics_test

import (
	"testing"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/metrics"
)

func TestReadsSomething(t *testing.T) {
	dir := t.TempDir()
	c := metrics.New(func() string { return dir })
	time.Sleep(50 * time.Millisecond)
	m := c.Read()
	if m.GetMemoryTotalBytes() == 0 || m.GetPoolTotalBytes() == 0 || m.GetPoolFallback() {
		t.Fatalf("unexpected reading %+v", m)
	}
	if m.GetCpuPercent() < 0 || m.GetCpuPercent() > 100 {
		t.Fatalf("cpu %v", m.GetCpuPercent())
	}
}

func TestReportsDrives(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	c := metrics.New(func() string { return "" })
	c.Drives = func() map[string]string { return map[string]string{"sdb": b, "sda": a, "gone": "/does/not/exist"} }
	m := c.Read()
	if len(m.GetDrives()) != 2 || m.GetDrives()[0].GetDriveId() != "sda" || m.GetDrives()[1].GetDriveId() != "sdb" {
		t.Fatalf("drives %+v", m.GetDrives())
	}
	if d := m.GetDrives()[0]; d.GetTotalBytes() == 0 || d.GetUsedBytes() > d.GetTotalBytes() {
		t.Fatalf("drive %+v", d)
	}
}
