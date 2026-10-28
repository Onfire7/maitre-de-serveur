package history

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func clock(s *Store, t *time.Time) { s.now = func() time.Time { return *t } }

// fill records v(i) every 10 s for n readings from at.
func fill(t *testing.T, s *Store, at time.Time, n int, v func(i int) float64) time.Time {
	t.Helper()
	for i := range n {
		if err := s.Record(at, map[string]float64{"node/cpu": v(i)}); err != nil {
			t.Fatal(err)
		}
		at = at.Add(10 * time.Second)
	}
	return at
}

func nans(xs []float64) int {
	c := 0
	for _, x := range xs {
		if math.IsNaN(x) {
			c++
		}
	}
	return c
}

func TestAveragesAndHighs(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := fill(t, s, t0, 360, func(i int) float64 { return float64(i % 6 * 10) }) // 0..50 each minute
	clock(s, &now)
	got, err := s.Query([]string{"node/cpu"}, t0.UnixMilli(), now.UnixMilli(), 60)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	g := got[0]
	if g.GetStepMs() != 60_000 || len(g.GetAvg()) != 60 {
		t.Fatalf("step %d, %d points", g.GetStepMs(), len(g.GetAvg()))
	}
	for i, a := range g.GetAvg() {
		if a != 25 || g.GetMax()[i] != 50 {
			t.Fatalf("point %d: avg %v max %v", i, a, g.GetMax()[i])
		}
	}
}

func TestGapsAreNaN(t *testing.T) {
	s, _ := Open(t.TempDir())
	at := fill(t, s, t0, 60, func(int) float64 { return 1 })
	at = fill(t, s, at.Add(30*time.Minute), 60, func(int) float64 { return 2 })
	clock(s, &at)
	got, _ := s.Query([]string{"node/cpu"}, t0.UnixMilli(), at.UnixMilli(), 1000)
	g := got[0]
	if g.GetStepMs() != 10_000 || nans(g.GetAvg()) != 180 || len(g.GetAvg()) != 300 {
		t.Fatalf("step %d, %d points, %d gaps", g.GetStepMs(), len(g.GetAvg()), nans(g.GetAvg()))
	}
	if !math.IsNaN(g.GetAvg()[100]) || g.GetAvg()[0] != 1 || g.GetAvg()[299] != 2 {
		t.Fatalf("avg %v", g.GetAvg())
	}
}

func TestSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	// 90 minutes: the hour in progress is only in the accumulator.
	at := fill(t, s, t0, 540, func(int) float64 { return 7 })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at = fill(t, s, at, 180, func(int) float64 { return 7 })
	clock(s, &at)
	for _, from := range []time.Time{at.Add(-2 * time.Hour), at.Add(-30 * 24 * time.Hour), at.Add(-365 * 24 * time.Hour)} {
		got, _ := s.Query([]string{"node/cpu"}, from.UnixMilli(), 0, 300)
		g := got[0]
		ok := 0
		for _, a := range g.GetAvg() {
			if a == 7 {
				ok++
			} else if !math.IsNaN(a) {
				t.Fatalf("from %v: unexpected %v", from, a)
			}
		}
		if ok == 0 {
			t.Fatalf("from %v: no readings (step %d)", from, g.GetStepMs())
		}
	}
	// The hourly level counted both halves of its first hour.
	l := s.series["node/cpu"].levels[3]
	if l.last != t0.UnixMilli() || l.acc.n != 360 {
		t.Fatalf("hourly last %d acc %+v", l.last, l.acc)
	}
}

func TestOldClockAndWraparound(t *testing.T) {
	s, _ := Open(t.TempDir())
	at := fill(t, s, t0, 10, func(int) float64 { return 1 })
	fill(t, s, t0, 3, func(int) float64 { return 99 }) // clock went back: dropped
	// Longer than level 0 keeps: its oldest steps are gone, the rest are new.
	at = fill(t, s, at, 2500, func(int) float64 { return 3 })
	clock(s, &at)
	got, _ := s.Query([]string{"node/cpu"}, at.Add(-6*time.Hour).UnixMilli()+10_000, 0, 10_000)
	g := got[0]
	if g.GetStepMs() != 10_000 {
		t.Fatalf("step %d", g.GetStepMs())
	}
	for i, a := range g.GetAvg() {
		if a != 3 && !math.IsNaN(a) {
			t.Fatalf("point %d = %v", i, a)
		}
	}
}

func TestPrefixesAndSweep(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	now := t0
	clock(s, &now)
	_ = s.Record(t0, map[string]float64{"node/cpu": 1, "drive/sda": 2, "service/jellyfin/cpu": 3, "service/jellyfin/memory": 4, "bad name": 5, "../x": 6})
	names := func(q ...string) []string {
		got, _ := s.Query(q, t0.UnixMilli(), t0.UnixMilli()+1, 10)
		var out []string
		for _, g := range got {
			out = append(out, g.GetName())
		}
		return out
	}
	if n := names(); len(n) != 2 || n[0] != "drive/sda" || n[1] != "node/cpu" {
		t.Fatalf("default %v", n)
	}
	if n := names("service/jellyfin/"); len(n) != 2 {
		t.Fatalf("prefix %v", n)
	}
	now = t0.Add(Keep / 2)
	_ = s.Record(now, map[string]float64{"node/cpu": 1})
	now = t0.Add(Keep + time.Hour)
	s.Sweep()
	if len(s.series) != 1 {
		t.Fatalf("after sweep %d series", len(s.series))
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "service", "*")); len(m) != 0 {
		t.Fatalf("left %v", m)
	}
}

func TestExportImport(t *testing.T) {
	a, _ := Open(t.TempDir())
	b, _ := Open(t.TempDir())
	_ = a.Record(t0, map[string]float64{"service/jellyfin/cpu": 12, "service/other/cpu": 1})
	_ = b.Record(t0.Add(time.Minute), map[string]float64{"service/jellyfin/cpu": 99})
	files, err := a.Export("jellyfin")
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	if err := b.Import("jellyfin", files); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(time.Minute)
	clock(b, &now)
	got, _ := b.Query([]string{"service/jellyfin/cpu"}, t0.UnixMilli(), 0, 100)
	if len(got) != 1 || got[0].GetAvg()[0] != 12 {
		t.Fatalf("%v", got)
	}
	if err := b.Import("jellyfin", map[string][]byte{"cpu": []byte("nope")}); err == nil {
		t.Fatal("took a bad file")
	}
	if err := b.Import("jellyfin", map[string][]byte{"../x": files["cpu"]}); err == nil {
		t.Fatal("took a bad name")
	}
}

func TestMergeAligns(t *testing.T) {
	nan := math.NaN()
	start, avg, hi := merge(30, 10, []float64{1, 2, nan, 4, 5}, []float64{1, 2, nan, 4, 9}, 2)
	if start != 20 || len(avg) != 3 || avg[0] != 1 || avg[1] != 2 || avg[2] != 4.5 || hi[2] != 9 {
		t.Fatalf("start %d avg %v hi %v", start, avg, hi)
	}
}
