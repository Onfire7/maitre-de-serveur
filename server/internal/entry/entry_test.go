package entry

import (
	"context"
	"errors"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

func on(node string) *maitrev1.EntryNode { return &maitrev1.EntryNode{Enabled: true, NodeId: node} }

func TestElect(t *testing.T) {
	a := Candidate{ID: "a", Reachable: true, Busy: 2}
	b := Candidate{ID: "b", Reachable: true, Busy: 0}
	c := Candidate{ID: "c", Reachable: true, Busy: 0}
	down := func(x Candidate) Candidate { x.Reachable = false; return x }
	holding := func(x Candidate, busy int) Candidate { x.Holds = []string{Web}; x.Busy = busy; return x }
	for _, tc := range []struct {
		name  string
		e     *maitrev1.EntryNode
		nodes []Candidate
		want  string
	}{
		{"off", &maitrev1.EntryNode{}, []Candidate{a, b}, ""},
		{"nobody up", on(""), []Candidate{down(a), down(b)}, ""},
		{"least busy, then lowest id", on(""), []Candidate{a, c, b}, "b"},
		{"pinned", on("a"), []Candidate{a, b}, "a"},
		{"pinned but down: elected", on("a"), []Candidate{down(a), b}, "b"},
		{"holder kept when a little busier", on(""), []Candidate{holding(a, 1), b}, "a"},
		{"holder loses it when much busier", on(""), []Candidate{holding(a, 2), b}, "b"},
		{"holder down", on(""), []Candidate{down(holding(a, 0)), c, b}, "b"},
		{"two holders settle on the quieter", on(""), []Candidate{holding(a, 1), holding(c, 0), b}, "c"},
		{"another role's holder does not count", on(""), []Candidate{{ID: "a", Reachable: true, Busy: 1, Holds: []string{App}}, b}, "b"},
	} {
		if got := Elect(tc.e, Web, tc.nodes); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBusyness(t *testing.T) {
	for _, tc := range []struct {
		m    *maitrev1.NodeMetrics
		want int
	}{
		{nil, 0},
		{&maitrev1.NodeMetrics{CpuPercent: 19}, 0},
		{&maitrev1.NodeMetrics{CpuPercent: 45}, 2},
		{&maitrev1.NodeMetrics{CpuPercent: 5, MemoryTotalBytes: 100, MemoryUsedBytes: 81}, 4},
	} {
		if got := Busyness(tc.m); got != tc.want {
			t.Errorf("Busyness(%v) = %d, want %d", tc.m, got, tc.want)
		}
	}
}

func TestCheckDNS(t *testing.T) {
	recs := map[string][]string{"home.example.com": {"203.0.113.7"}, "maitre-check.home.example.com": {"203.0.113.7"}, "old.example.com": {"198.51.100.1"}}
	lookup := func(_ context.Context, h string) ([]string, error) {
		if a, ok := recs[h]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	ctx := context.Background()
	for _, tc := range []struct {
		domain, ip string
		wildcard   bool
		want       string
	}{
		{"home.example.com", "203.0.113.7", true, ""},
		{"home.example.com", "", true, ""},
		{"old.example.com", "203.0.113.7", false, "old.example.com points at 198.51.100.1, not at 203.0.113.7"},
		{"new.example.com", "203.0.113.7", false, "waiting for DNS: point new.example.com at 203.0.113.7"},
		{"new.example.com", "", false, "waiting for DNS: no record for new.example.com yet"},
	} {
		if got := CheckDNS(ctx, lookup, tc.domain, tc.ip, tc.wildcard); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.domain, got, tc.want)
		}
	}
	// The wildcard is checked for the web.
	delete(recs, "maitre-check.home.example.com")
	if got := CheckDNS(ctx, lookup, "home.example.com", "203.0.113.7", true); got != "waiting for DNS: point *.home.example.com at 203.0.113.7" {
		t.Errorf("wildcard: %q", got)
	}
}
