package rollout

import (
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

func TestDecide(t *testing.T) {
	policy := &maitrev1.UpdatePolicy{Automatic: true, WindowStartHour: 3, WindowEndHour: 5, StageHours: 24}
	at := func(h int) time.Time { return time.Date(2026, 10, 26, h, 0, 0, 0, time.Local) }
	nodes := []*maitrev1.Node{
		{NodeId: "aaa", Name: "alpha", ImageDigest: "old", BootedUnixMs: at(0).Add(-48 * time.Hour).UnixMilli()},
		{NodeId: "bbb", Name: "beta", ImageDigest: "old"},
	}
	if d := Decide(policy, nodes, "aaa", "old", "old", at(4)); d.Update || d.Reason != "up to date" {
		t.Errorf("up to date: %+v", d)
	}
	if d := Decide(policy, nodes, "aaa", "old", "new", at(12)); d.Update {
		t.Errorf("outside window: %+v", d)
	}
	if d := Decide(policy, nodes, "aaa", "old", "new", at(4)); !d.Update {
		t.Errorf("first node should update: %+v", d)
	}
	if d := Decide(policy, nodes, "bbb", "old", "new", at(4)); d.Update {
		t.Errorf("second node must wait for the first: %+v", d)
	}
	nodes[0].ImageDigest = "new"
	nodes[0].BootedUnixMs = at(4).Add(-2 * time.Hour).UnixMilli()
	if d := Decide(policy, nodes, "bbb", "old", "new", at(4)); d.Update {
		t.Errorf("second node must wait out the stage: %+v", d)
	}
	nodes[0].BootedUnixMs = at(4).Add(-30 * time.Hour).UnixMilli()
	if d := Decide(policy, nodes, "bbb", "old", "new", at(4)); !d.Update {
		t.Errorf("second node should follow: %+v", d)
	}
	policy.Paused = true
	if d := Decide(policy, nodes, "bbb", "old", "new", at(4)); d.Update {
		t.Errorf("paused: %+v", d)
	}
}
