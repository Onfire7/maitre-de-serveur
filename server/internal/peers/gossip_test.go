package peers

import (
	"log/slog"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

func TestReceivedMarksPeerHeard(t *testing.T) {
	m := &Manager{log: slog.Default(), peers: map[string]*peer{"b": {id: "b", name: "Basalt"}}, heard: map[string]*maitrev1.FleetNodeStatus{}}
	if m.Last("b") != nil {
		t.Fatal("nothing heard yet")
	}
	m.Received(nil)
	m.Received(&maitrev1.FleetNodeStatus{})
	if len(m.heard) != 0 {
		t.Fatal("empty gossip must be dropped")
	}
	before := time.Now()
	m.Received(&maitrev1.FleetNodeStatus{NodeId: "b", MetricsUnixMs: 42})
	if got := m.Last("b"); got == nil || got.GetMetricsUnixMs() != 42 {
		t.Fatalf("last = %v", got)
	}
	p := m.peers["b"]
	if !p.online || p.lastSeen.Before(before) {
		t.Fatal("a peer that gossips to us is online and just seen")
	}
	// A Node we do not know yet is kept too; Refresh will add the peer.
	m.Received(&maitrev1.FleetNodeStatus{NodeId: "z"})
	if m.Last("z") == nil {
		t.Fatal("unknown sender's status kept")
	}
}
