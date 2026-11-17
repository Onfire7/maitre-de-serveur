package update

import (
	"slices"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// How long a Node takes to update (docs/todo.md, "Say how long this Node
// takes to update"), from its own runs only: machines differ, so a figure
// is never borrowed from another Node. This is the one implementation; the
// Console only shows what GetUpdateStatus reports, and the rollout line
// uses it on the first Node's record.

// TypicalOf is how many of a Node's latest updated runs a figure takes,
// and TypicalAtLeast how many it needs to give one at all.
const (
	TypicalOf      = 5
	TypicalAtLeast = 3
)

// Typical is how long n's updates usually take from start to back up
// (total), and how long its Services are usually down in one, from the
// restart to back up (downtime): the medians of its last TypicalOf runs
// that ended updated, runs oldest first. Rolled-back runs restart twice and
// staged ones never, so neither counts. The downtime takes only runs that
// recorded the restart; older ones count toward the total only. Either is
// 0, no figure, with fewer than TypicalAtLeast runs to take it from.
func Typical(runs []*maitrev1.UpdateRun) (total, downtime time.Duration) {
	var totals, downs []time.Duration
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		if r.GetOutcome() != maitrev1.UpdateRun_OUTCOME_UPDATED {
			continue
		}
		end := r.GetEndedUnixMs()
		if len(totals) < TypicalOf {
			totals = append(totals, ms(end-r.GetStartedUnixMs()))
		}
		if len(downs) < TypicalOf && r.GetRestartedUnixMs() != 0 {
			downs = append(downs, ms(end-r.GetRestartedUnixMs()))
		}
	}
	return median(totals), median(downs)
}

func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

// median of d, 0 with fewer than TypicalAtLeast.
func median(d []time.Duration) time.Duration {
	if len(d) < TypicalAtLeast {
		return 0
	}
	slices.Sort(d)
	if m := len(d) / 2; len(d)%2 == 1 {
		return d[m]
	} else {
		return (d[m-1] + d[m]) / 2
	}
}
