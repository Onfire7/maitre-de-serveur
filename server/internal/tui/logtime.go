package tui

import (
	"math"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// HistorySource is a Source that also reads the Node's stored metrics
// (decision 210). Without it the charts show only what the screen saw.
type HistorySource interface {
	History(names []string, from, to time.Time, points int) ([]*maitrev1.MetricSeries, error)
}

// The charts span a day on a logarithmic scale (decision 211): the last
// column is the latest reading, the others cover from 20 s to 24 hours
// ago, each about a third longer than the one to its right, so the last
// minutes get as much room as the hours before them.
const (
	logNewest = 20 * time.Second
	logSpan   = 24 * time.Hour
	// fineSpan is what the Node keeps at 10 s; older columns read the
	// 1-minute level.
	fineSpan = 6*time.Hour - 20*time.Second
)

// logEdge is the age of the newer edge of column c (0 is the oldest, the
// last column is the latest reading).
func logEdge(c int) time.Duration {
	k := float64(histLen-2-c) / float64(histLen-1)
	return time.Duration(float64(logNewest) * math.Pow(float64(logSpan)/float64(logNewest), k))
}

// logColumns averages a series into the chart's columns: each point counts
// by how much of its step falls in the column. fine holds the last
// fineSpan at 10 s, coarse the day at 1 minute; a column with no readings
// is NaN. The last column is live.
func logColumns(fine, coarse *maitrev1.MetricSeries, now time.Time, live float64) []float64 {
	out := make([]float64, histLen)
	for c := range histLen - 1 {
		newer, older := logEdge(c), logEdge(c-1)
		from, to := now.Add(-older).UnixMilli(), now.Add(-newer).UnixMilli()
		s := coarse
		if older <= fineSpan && fine != nil {
			s = fine
		}
		out[c] = overlapAvg(s, from, to)
	}
	out[histLen-1] = live
	return out
}

func overlapAvg(s *maitrev1.MetricSeries, from, to int64) float64 {
	if s == nil || s.GetStepMs() <= 0 {
		return math.NaN()
	}
	var sum, weight float64
	for i, a := range s.GetAvg() {
		if math.IsNaN(a) {
			continue
		}
		t0 := s.GetStartUnixMs() + int64(i)*s.GetStepMs()
		t1 := t0 + s.GetStepMs()
		if w := min(t1, to) - max(t0, from); w > 0 {
			sum += a * float64(w)
			weight += float64(w)
		}
	}
	if weight == 0 {
		return math.NaN()
	}
	return sum / weight
}

// logAxis is the line under a chart: where 6 hours, 1 hour, 10 minutes and
// 1 minute ago fall, between "24h" on the left and "now" on the right.
func logAxis() string {
	row := []rune(strings.Repeat(" ", histLen+2))
	put := func(at int, label string) {
		r := []rune(label)
		at = max(0, min(len(row)-len(r), at))
		for i := range r {
			if row[at+i] != ' ' || (at+i > 0 && row[at+i-1] != ' ') || (at+i+1 < len(row) && i == len(r)-1 && row[at+i+1] != ' ') {
				return
			}
		}
		copy(row[at:], r)
	}
	put(0, "24h")
	put(len(row)-3, "now")
	for _, m := range []struct {
		age   time.Duration
		label string
	}{{6 * time.Hour, "6h"}, {time.Hour, "1h"}, {10 * time.Minute, "10m"}, {time.Minute, "1m"}} {
		// The column holding that age, plus one for the chart's left edge.
		for c := range histLen - 1 {
			if logEdge(c) <= m.age {
				put(c+1-len(m.label)/2, m.label)
				break
			}
		}
	}
	return string(row)
}
