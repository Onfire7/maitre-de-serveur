package nodeservice

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

func TestGetNodeLogs(t *testing.T) {
	var got []string
	old := Journal
	Journal = func(_ context.Context, args ...string) (string, error) {
		got = args
		return "2026-10-27T10:00:00 node maitre[1]: one\n2026-10-27T10:00:01 node maitre[1]: two\n", nil
	}
	defer func() { Journal = old }()
	s := &Service{Services: func(context.Context) []*maitrev1.ServiceStatus {
		return []*maitrev1.ServiceStatus{{Spec: &maitrev1.ServiceSpec{Name: "jellyfin"}}}
	}}
	ctx := context.Background()

	res, err := s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetLines()) != 2 || strings.Join(got, " ") != "-u maitre-agent.service -n 300 --no-pager -o short-iso" {
		t.Fatalf("agent: %v %v", res.GetLines(), got)
	}
	if ids := res.GetSources(); ids[0].GetId() != "agent" || ids[len(ids)-1].GetId() != "service:jellyfin" {
		t.Fatalf("sources %v", ids)
	}
	if _, err := s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: "service:jellyfin", Lines: 9999}); err != nil || strings.Join(got, " ") != "-u jellyfin.service -n 5000 --no-pager -o short-iso" {
		t.Fatalf("service: %v %v", err, got)
	}
	// A Service of several components: whole, or one component.
	s.Services = func(context.Context) []*maitrev1.ServiceStatus {
		return []*maitrev1.ServiceStatus{{Spec: &maitrev1.ServiceSpec{Name: "notes", Components: []*maitrev1.ServiceComponent{{Name: "app"}, {Name: "db"}}}}}
	}
	res, _ = s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{})
	if ids := res.GetSources(); ids[len(ids)-1].GetId() != "service:notes/db" || ids[len(ids)-1].GetLabel() != "notes: db" {
		t.Fatalf("component sources %v", ids)
	}
	if _, err := s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: "service:notes"}); err != nil || strings.Join(got, " ") != "-u notes-app.service -u notes-db.service -n 300 --no-pager -o short-iso" {
		t.Fatalf("whole: %v %v", err, got)
	}
	if _, err := s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: "service:notes/db"}); err != nil || strings.Join(got, " ") != "-u notes-db.service -n 300 --no-pager -o short-iso" {
		t.Fatalf("component: %v %v", err, got)
	}
	// Only listed sources: no arbitrary units or journalctl flags.
	for _, bad := range []string{"sshd.service", "service:nope", "-f"} {
		if _, err := s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: bad}); err == nil {
			t.Fatalf("read %q", bad)
		}
	}
}

// The boot before this one is the one worth reading after a crash, and a
// Node that has no such boot says so in a way that stops the asking.
func TestGetNodeLogsReadsThePreviousBoot(t *testing.T) {
	var got []string
	fail := false
	old := Journal
	Journal = func(_ context.Context, args ...string) (string, error) {
		got = args
		if fail {
			return "Data from the specified boot (-1) is not available", errors.New("exit status 1")
		}
		return "2026-11-06T03:14:00 avalon kernel: Kernel panic - not syncing\n", nil
	}
	defer func() { Journal = old }()
	s := &Service{}
	ctx := context.Background()

	res, err := s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: "kernel-prev"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "-k -b -1 -n 300 --no-pager -o short-iso" {
		t.Fatalf("args %v", got)
	}
	if len(res.GetLines()) != 1 || !strings.Contains(res.GetLines()[0], "Kernel panic") {
		t.Fatalf("lines %v", res.GetLines())
	}
	if _, err := s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: "system-prev"}); err != nil || strings.Join(got, " ") != "-b -1 -n 300 --no-pager -o short-iso" {
		t.Fatalf("system-prev: %v %v", err, got)
	}
	// Both are offered, next to the boot they are the previous one of.
	var ids []string
	for _, src := range res.GetSources() {
		ids = append(ids, src.GetId())
	}
	if !slices.Contains(ids, "kernel-prev") || !slices.Contains(ids, "system-prev") {
		t.Fatalf("sources %v", ids)
	}
	// No previous boot is permanent, so it is NotFound and not Unavailable.
	fail = true
	_, err = s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: "kernel-prev"})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing boot: %v (%v)", err, connect.CodeOf(err))
	}
	// This boot still fails the way it always did.
	_, err = s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: "kernel"})
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("this boot: %v (%v)", err, connect.CodeOf(err))
	}
}
