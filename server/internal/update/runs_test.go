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
	Begin(n, "depot/maitre:dev", "sha256:a", "boot-1", "device-abc", t0)
	if !Back(n, "boot-2", "sha256:b", time.Time{}, t0.Add(4*time.Minute)) || n.GetUpdating() != nil {
		t.Fatal("not closed")
	}
	r := n.GetUpdates()[0]
	if r.GetOutcome() != maitrev1.UpdateRun_OUTCOME_UPDATED || r.GetEndedUnixMs()-r.GetStartedUnixMs() != 240_000 || r.GetToDigest() != "sha256:b" || r.GetTarget() != "depot/maitre:dev" {
		t.Fatalf("%v", r)
	}
	// Same boot at the next start: the agent restarted, the update failed.
	Begin(n, "x", "sha256:b", "boot-2", ByRollout, t0.Add(time.Hour))
	Back(n, "boot-2", "sha256:b", time.Time{}, t0.Add(time.Hour+time.Minute))
	// A rollback after it started; an older one does not count.
	Begin(n, "x", "sha256:b", "boot-2", ByRollout, t0.Add(2*time.Hour))
	Back(n, "boot-3", "sha256:b", t0.Add(2*time.Hour+3*time.Minute), t0.Add(2*time.Hour+5*time.Minute))
	Begin(n, "x", "sha256:b", "boot-3", ByRollout, t0.Add(3*time.Hour))
	Back(n, "boot-4", "sha256:c", t0, t0.Add(3*time.Hour+5*time.Minute))
	want := []maitrev1.UpdateRun_Outcome{maitrev1.UpdateRun_OUTCOME_UPDATED, maitrev1.UpdateRun_OUTCOME_FAILED, maitrev1.UpdateRun_OUTCOME_ROLLED_BACK, maitrev1.UpdateRun_OUTCOME_UPDATED}
	for i, w := range want {
		if got := n.GetUpdates()[i].GetOutcome(); got != w {
			t.Errorf("run %d: %v, want %v", i, got, w)
		}
	}
	for range 30 {
		Begin(n, "x", "", "", "", t0)
		End(n, maitrev1.UpdateRun_OUTCOME_STAGED, "", "", t0)
	}
	if len(n.GetUpdates()) != KeepRuns {
		t.Fatalf("%d runs kept", len(n.GetUpdates()))
	}
}

// A finished run says who asked for it, carried from Begin through End:
// without it a version that changed is traceable to nobody, which is the
// question Avalon could not answer on 2026-11-06.
func TestARunRemembersWhoAskedForIt(t *testing.T) {
	n := &maitrev1.Node{}
	t0 := time.Unix(1700000000, 0)
	Begin(n, "depot/maitre:dev", "sha256:a", "boot-1", "fingerprint-of-a-device", t0)
	if n.GetUpdating().GetRequestedBy() != "fingerprint-of-a-device" {
		t.Fatalf("in progress: %q", n.GetUpdating().GetRequestedBy())
	}
	if !End(n, maitrev1.UpdateRun_OUTCOME_STAGED, "sha256:b", "", t0.Add(time.Minute)) {
		t.Fatal("End reported no update in progress")
	}
	got := n.GetUpdates()[len(n.GetUpdates())-1]
	if got.GetRequestedBy() != "fingerprint-of-a-device" {
		t.Errorf("finished run: %q", got.GetRequestedBy())
	}
	// The Fleet's own rollout is named, not left blank, so an update
	// nobody asked for does not read like one somebody did.
	Begin(n, "depot/maitre:dev", "sha256:b", "boot-2", ByRollout, t0.Add(time.Hour))
	End(n, maitrev1.UpdateRun_OUTCOME_UPDATED, "sha256:c", "", t0.Add(2*time.Hour))
	if got := n.GetUpdates()[len(n.GetUpdates())-1].GetRequestedBy(); got != ByRollout {
		t.Errorf("rollout run: %q", got)
	}
}
