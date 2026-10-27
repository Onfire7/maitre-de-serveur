package nodeservice

import (
	"context"
	"strings"
	"testing"

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
	// Only listed sources: no arbitrary units or journalctl flags.
	for _, bad := range []string{"sshd.service", "service:nope", "-f"} {
		if _, err := s.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: bad}); err == nil {
			t.Fatalf("read %q", bad)
		}
	}
}
