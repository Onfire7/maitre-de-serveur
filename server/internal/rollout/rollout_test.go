package rollout

import (
	"context"
	"errors"
	"strings"
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

func TestNewer(t *testing.T) {
	st := &maitrev1.UpdateStatus{Booted: &maitrev1.ImageDeployment{Digest: "sha256:a"}}
	if got := Newer(st, "sha256:a"); got != "" {
		t.Fatalf("booted digest is not newer, got %q", got)
	}
	if got := Newer(st, ""); got != "" {
		t.Fatalf("no answer is not newer, got %q", got)
	}
	if got := Newer(st, "sha256:b"); got != "sha256:b" {
		t.Fatalf("want sha256:b, got %q", got)
	}
	st.Staged = &maitrev1.ImageDeployment{Digest: "sha256:b"}
	if got := Newer(st, "sha256:b"); got != "" {
		t.Fatalf("staged digest is not newer, got %q", got)
	}
}

func TestCheck(t *testing.T) {
	r := &Runner{
		Status: func(context.Context) (*maitrev1.UpdateStatus, error) {
			return &maitrev1.UpdateStatus{Booted: &maitrev1.ImageDeployment{Image: "reg/maitre:stable", Digest: "sha256:a"}}, nil
		},
	}
	answer, fail := "sha256:b", false
	r.Digest = func(_ context.Context, ref string) (string, error) {
		if ref != "reg/maitre:stable" {
			t.Fatalf("looked up %q", ref)
		}
		if fail {
			return "", errors.New("registry down")
		}
		return answer, nil
	}
	r.Check(context.Background())
	if r.Available() != "sha256:b" {
		t.Fatalf("want sha256:b available, got %q", r.Available())
	}
	fail = true
	r.Check(context.Background())
	if r.Available() != "sha256:b" {
		t.Fatalf("a failed lookup should keep the last answer, got %q", r.Available())
	}
	fail, answer = false, "sha256:a"
	r.Check(context.Background())
	if r.Available() != "" {
		t.Fatalf("up to date, got %q", r.Available())
	}
}

// updated is n updated runs of the given length, oldest first.
func updated(n int, took time.Duration) []*maitrev1.UpdateRun {
	var runs []*maitrev1.UpdateRun
	for i := range n {
		start := int64(i) * int64(time.Hour/time.Millisecond)
		runs = append(runs, &maitrev1.UpdateRun{Outcome: maitrev1.UpdateRun_OUTCOME_UPDATED, StartedUnixMs: start, EndedUnixMs: start + took.Milliseconds()})
	}
	return runs
}

func TestTheWaitingLineSaysHowLongTheFirstNodeUsuallyTakes(t *testing.T) {
	policy := &maitrev1.UpdatePolicy{Automatic: true, StageHours: 24}
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.Local)
	first := &maitrev1.Node{NodeId: "aaa", Name: "Avalon", ImageDigest: "old", Updates: updated(5, 6*time.Minute+10*time.Second)}
	nodes := []*maitrev1.Node{first, {NodeId: "bbb", Name: "Basalt", ImageDigest: "old", Updates: updated(5, 3*time.Minute)}}

	want := "an update is ready; waiting for Avalon to take it first (usually about 6 minutes)"
	if d := Decide(policy, nodes, "bbb", "old", "new", now); d.Reason != want {
		t.Errorf("got %q, want %q", d.Reason, want)
	}

	first.Updates = updated(5, 40*time.Second)
	want = "an update is ready; waiting for Avalon to take it first (usually under a minute)"
	if d := Decide(policy, nodes, "bbb", "old", "new", now); d.Reason != want {
		t.Errorf("got %q, want %q", d.Reason, want)
	}

	first.Updates = updated(5, 70*time.Second)
	want = "an update is ready; waiting for Avalon to take it first (usually about 1 minute)"
	if d := Decide(policy, nodes, "bbb", "old", "new", now); d.Reason != want {
		t.Errorf("got %q, want %q", d.Reason, want)
	}
}

func TestTheWaitingLineIsUnchangedWithoutAFigure(t *testing.T) {
	policy := &maitrev1.UpdatePolicy{Automatic: true, StageHours: 24}
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.Local)
	// Two updated runs is too few for a figure; Basalt's own history is not borrowed.
	nodes := []*maitrev1.Node{
		{NodeId: "aaa", Name: "Avalon", ImageDigest: "old", Updates: updated(2, 6*time.Minute)},
		{NodeId: "bbb", Name: "Basalt", ImageDigest: "old", Updates: updated(5, 3*time.Minute)},
	}
	want := "an update is ready; waiting for Avalon to take it first"
	if d := Decide(policy, nodes, "bbb", "old", "new", now); d.Reason != want {
		t.Errorf("got %q, want %q", d.Reason, want)
	}
}

func TestNoOtherRolloutLineGainsAnEstimate(t *testing.T) {
	policy := &maitrev1.UpdatePolicy{Automatic: true, WindowStartHour: 3, WindowEndHour: 5, StageHours: 24}
	at := func(h int) time.Time { return time.Date(2026, 10, 26, h, 0, 0, 0, time.Local) }
	nodes := []*maitrev1.Node{
		{NodeId: "aaa", Name: "Avalon", ImageDigest: "old", Updates: updated(5, 6*time.Minute)},
		{NodeId: "bbb", Name: "Basalt", ImageDigest: "old", Updates: updated(5, 6*time.Minute)},
	}
	check := func(what string, d Decision) {
		t.Helper()
		if strings.Contains(d.Reason, "usually") || strings.Contains(d.Reason, "minute") {
			t.Errorf("%s: %q gained an estimate", what, d.Reason)
		}
	}
	check("up to date", Decide(policy, nodes, "bbb", "old", "old", at(4)))
	check("outside window", Decide(policy, nodes, "bbb", "old", "new", at(12)))
	check("first Node", Decide(policy, nodes, "aaa", "old", "new", at(4)))
	nodes[0].ImageDigest = "new"
	nodes[0].BootedUnixMs = at(4).Add(-2 * time.Hour).UnixMilli()
	check("staging", Decide(policy, nodes, "bbb", "old", "new", at(4)))
	nodes[0].BootedUnixMs = at(4).Add(-30 * time.Hour).UnixMilli()
	check("following", Decide(policy, nodes, "bbb", "old", "new", at(4)))
	policy.Paused = true
	check("paused", Decide(policy, nodes, "bbb", "old", "new", at(4)))
}
