package metrics_test

import (
	"os"
	"path/filepath"
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

func TestReadsUnitCgroup(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "system.slice", "jellyfin.service")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 1500000\nuser_usec 1000000\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "memory.current"), []byte("1000\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "memory.stat"), []byte("anon 600\ninactive_file 300\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "io.stat"), []byte("8:0 rbytes=10 wbytes=20 rios=1\n8:16 rbytes=5 wbytes=0\n"), 0o644)
	metrics.CgroupRoot = root
	u, ok := metrics.Unit("jellyfin.service")
	if !ok || u != (metrics.UnitUsage{CPUMicros: 1500000, Memory: 700, ReadBytes: 15, WriteBytes: 20}) {
		t.Fatalf("%v %+v", ok, u)
	}
	if _, ok := metrics.Unit("gone.service"); ok {
		t.Fatal("a missing unit read as running")
	}
}
