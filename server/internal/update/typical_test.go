package update

import (
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// run is one finished update: total minutes from start to back up, and the
// minutes it was down (restart to back up); down < 0 is a run from before
// the restart was recorded.
func run(outcome maitrev1.UpdateRun_Outcome, total, down float64) *maitrev1.UpdateRun {
	start := int64(1_700_000_000_000)
	end := start + int64(total*60_000)
	r := &maitrev1.UpdateRun{StartedUnixMs: start, EndedUnixMs: end, Outcome: outcome}
	if down >= 0 {
		r.RestartedUnixMs = end - int64(down*60_000)
	}
	return r
}

const (
	updated    = maitrev1.UpdateRun_OUTCOME_UPDATED
	rolledBack = maitrev1.UpdateRun_OUTCOME_ROLLED_BACK
	staged     = maitrev1.UpdateRun_OUTCOME_STAGED
	failed     = maitrev1.UpdateRun_OUTCOME_FAILED
	older      = -1
)

func TestTypical(t *testing.T) {
	for _, tc := range []struct {
		name        string
		runs        []*maitrev1.UpdateRun // oldest first
		total, down time.Duration         // 0: no figure
	}{
		{name: "no history"},
		{name: "two updated runs are too few",
			runs: []*maitrev1.UpdateRun{run(updated, 6, 2), run(updated, 7, 2)}},
		{name: "three updated runs: their medians",
			runs:  []*maitrev1.UpdateRun{run(updated, 6, 2), run(updated, 9, 3), run(updated, 5, 1)},
			total: 6 * time.Minute, down: 2 * time.Minute},
		{name: "four: the middle two averaged",
			runs:  []*maitrev1.UpdateRun{run(updated, 4, 1), run(updated, 6, 2), run(updated, 8, 3), run(updated, 10, 4)},
			total: 7 * time.Minute, down: 150 * time.Second},
		{name: "only the last five updated runs count",
			runs: []*maitrev1.UpdateRun{
				run(updated, 60, 30), run(updated, 60, 30), run(updated, 60, 30),
				run(updated, 6, 2), run(updated, 6, 2), run(updated, 6, 2), run(updated, 60, 30), run(updated, 6, 2)},
			total: 6 * time.Minute, down: 2 * time.Minute},
		{name: "rolled-back, staged and failed runs do not count",
			runs: []*maitrev1.UpdateRun{
				run(updated, 6, 2), run(rolledBack, 30, 20), run(staged, 40, older), run(updated, 6, 2),
				run(failed, 1, older), run(rolledBack, 30, 20), run(staged, 40, older)},
		},
		{name: "rolled-back, staged and failed runs do not count, with enough updated ones",
			runs: []*maitrev1.UpdateRun{
				run(updated, 6, 2), run(rolledBack, 30, 20), run(staged, 40, older), run(updated, 6, 2),
				run(failed, 1, older), run(rolledBack, 30, 20), run(updated, 6, 2), run(staged, 40, older)},
			total: 6 * time.Minute, down: 2 * time.Minute},
		{name: "older runs without a restart time count toward the total only",
			runs:  []*maitrev1.UpdateRun{run(updated, 8, older), run(updated, 8, older), run(updated, 6, 2), run(updated, 6, 2)},
			total: 7 * time.Minute},
		{name: "the downtime takes the last five updated runs with a restart time",
			runs: []*maitrev1.UpdateRun{
				run(updated, 6, 2), run(updated, 6, 2), run(updated, 6, 2),
				run(updated, 9, older), run(updated, 9, older), run(updated, 9, older)},
			total: 9 * time.Minute, down: 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, down := Typical(tc.runs)
			if total != tc.total || down != tc.down {
				t.Errorf("Typical = %v, %v; want %v, %v", total, down, tc.total, tc.down)
			}
		})
	}
}
