package update

import (
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

func TestRuns(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	n := &maitrev1.Node{}
	if Back(n, "boot-1", "sha256:a", time.Time{}, t0) || End(n, maitrev1.UpdateRun_OUTCOME_FAILED, "", "", t0) {
		t.Fatal("closed an update that never started")
	}
	// Rebooted into the new image: updated, four minutes.
	Begin(n, "depot/maitre:dev", "sha256:a", "boot-1", t0)
	if !Back(n, "boot-2", "sha256:b", time.Time{}, t0.Add(4*time.Minute)) || n.GetUpdating() != nil {
		t.Fatal("not closed")
	}
	r := n.GetUpdates()[0]
	if r.GetOutcome() != maitrev1.UpdateRun_OUTCOME_UPDATED || r.GetEndedUnixMs()-r.GetStartedUnixMs() != 240_000 || r.GetToDigest() != "sha256:b" || r.GetTarget() != "depot/maitre:dev" {
		t.Fatalf("%v", r)
	}
	// Same boot at the next start: the agent restarted, the update failed.
	Begin(n, "x", "sha256:b", "boot-2", t0.Add(time.Hour))
	Back(n, "boot-2", "sha256:b", time.Time{}, t0.Add(time.Hour+time.Minute))
	// A rollback after it started; an older one does not count.
	Begin(n, "x", "sha256:b", "boot-2", t0.Add(2*time.Hour))
	Back(n, "boot-3", "sha256:b", t0.Add(2*time.Hour+3*time.Minute), t0.Add(2*time.Hour+5*time.Minute))
	Begin(n, "x", "sha256:b", "boot-3", t0.Add(3*time.Hour))
	Back(n, "boot-4", "sha256:c", t0, t0.Add(3*time.Hour+5*time.Minute))
	want := []maitrev1.UpdateRun_Outcome{maitrev1.UpdateRun_OUTCOME_UPDATED, maitrev1.UpdateRun_OUTCOME_FAILED, maitrev1.UpdateRun_OUTCOME_ROLLED_BACK, maitrev1.UpdateRun_OUTCOME_UPDATED}
	for i, w := range want {
		if got := n.GetUpdates()[i].GetOutcome(); got != w {
			t.Errorf("run %d: %v, want %v", i, got, w)
		}
	}
	for range 30 {
		Begin(n, "x", "", "", t0)
		End(n, maitrev1.UpdateRun_OUTCOME_STAGED, "", "", t0)
	}
	if len(n.GetUpdates()) != KeepRuns {
		t.Fatalf("%d runs kept", len(n.GetUpdates()))
	}
}
