package tui

import (
	"math"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

func TestLogEdgesSpanTheDay(t *testing.T) {
	if logEdge(-1) != logSpan || logEdge(histLen-2) != logNewest {
		t.Fatalf("edges %v .. %v", logEdge(-1), logEdge(histLen-2))
	}
	for c := range histLen - 1 {
		if logEdge(c) >= logEdge(c-1) {
			t.Fatalf("column %d: %v not newer than %v", c, logEdge(c), logEdge(c-1))
		}
	}
}

// flat is a series of v from from to to at step, NaN where gap says.
func flat(from, to time.Time, step time.Duration, v float64, gap func(time.Time) bool) *maitrev1.MetricSeries {
	s := &maitrev1.MetricSeries{StartUnixMs: from.UnixMilli(), StepMs: step.Milliseconds()}
	for t := from; t.Before(to); t = t.Add(step) {
		x := v
		if gap != nil && gap(t) {
			x = math.NaN()
		}
		s.Avg, s.Max = append(s.Avg, x), append(s.Max, x)
	}
	return s
}

func TestLogColumns(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fine := flat(now.Add(-fineSpan), now, 10*time.Second, 10, nil)
	// The coarse level has a gap from 12 to 10 hours ago.
	coarse := flat(now.Add(-logSpan), now, time.Minute, 50, func(t time.Time) bool { return now.Sub(t) > 10*time.Hour && now.Sub(t) < 12*time.Hour })
	cols := logColumns(fine, coarse, now, 99)
	if len(cols) != histLen || cols[histLen-1] != 99 {
		t.Fatalf("cols %v", cols)
	}
	for c := range histLen - 1 {
		older := logEdge(c - 1)
		want := 50.0
		if older <= fineSpan {
			want = 10
		}
		if newer := logEdge(c); newer >= 10*time.Hour+time.Minute && older <= 12*time.Hour-time.Minute {
			if !math.IsNaN(cols[c]) {
				t.Errorf("column %d (%v-%v) = %v, want a gap", c, newer, older, cols[c])
			}
			continue
		}
		if !math.IsNaN(cols[c]) && cols[c] != want {
			t.Errorf("column %d (%v-%v) = %v, want %v", c, logEdge(c), older, cols[c], want)
		}
	}
	// No series at all: every column but the live one is blank.
	for c, v := range logColumns(nil, nil, now, 5)[:histLen-1] {
		if !math.IsNaN(v) {
			t.Fatalf("column %d = %v", c, v)
		}
	}
}

func TestLogAxis(t *testing.T) {
	a := logAxis()
	if len([]rune(a)) != histLen+2 || !strings.HasPrefix(a, "24h") || !strings.HasSuffix(a, "now") {
		t.Fatalf("%q", a)
	}
	for _, l := range []string{" 6h ", " 1h ", " 10m ", " 1m "} {
		if !strings.Contains(a, l) {
			t.Errorf("axis %q lacks %q", a, l)
		}
	}
	if strings.Index(a, "6h") > strings.Index(a, "1h") || strings.Index(a, "1h") > strings.Index(a, "10m") {
		t.Errorf("axis out of order: %q", a)
	}
}

type historySource struct{ fakeSource }

func (historySource) History(names []string, from, to time.Time, _ int) ([]*maitrev1.MetricSeries, error) {
	var out []*maitrev1.MetricSeries
	for _, n := range names {
		s := flat(from, to, time.Minute, 60, nil)
		s.Name = n
		out = append(out, s)
	}
	return out, nil
}

func TestStatusChartsUseHistory(t *testing.T) {
	m := newModel(historySource{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if strings.Contains(m.View(), "24h") {
		t.Fatal("axis before any history")
	}
	m.Update(m.fetchHistory(time.Now())())
	v := m.View()
	if strings.Count(v, "24h") != 2 || strings.Count(v, "now") != 2 {
		t.Fatalf("no axes:\n%s", v)
	}
	if m.fetchHistory(time.Now()) != nil {
		t.Fatal("history read again within historyEvery")
	}
	// A plain Source keeps the screen's own samples.
	if newModel(fakeSource{}).fetchHistory(time.Now()) != nil {
		t.Fatal("fetched history from a source without it")
	}
}
