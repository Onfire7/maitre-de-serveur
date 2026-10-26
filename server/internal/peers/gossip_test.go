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
	// Service statuses travel only on change or every 5 minutes; a plain
	// metrics sample keeps the statuses heard before.
	m.Received(&maitrev1.FleetNodeStatus{NodeId: "b", Services: []*maitrev1.ServiceStatus{{Spec: &maitrev1.ServiceSpec{Name: "uptime"}}}, ServicesUnixMs: 7, MetricsUnixMs: 50})
	m.Received(&maitrev1.FleetNodeStatus{NodeId: "b", MetricsUnixMs: 60})
	if got := m.Last("b"); got.GetMetricsUnixMs() != 60 || got.GetServicesUnixMs() != 7 || len(got.GetServices()) != 1 {
		t.Fatalf("metrics-only sample must keep the last services: %v", got)
	}
	// A Node we do not know yet is kept too; Refresh will add the peer.
	m.Received(&maitrev1.FleetNodeStatus{NodeId: "z"})
	if m.Last("z") == nil {
		t.Fatal("unknown sender's status kept")
	}
}

func TestSilentPeerGoesOffline(t *testing.T) {
	m := &Manager{log: slog.Default(), peers: map[string]*peer{"b": {id: "b", name: "Basalt", online: true, lastSeen: time.Now().Add(-2 * SilentFor)}}, heard: map[string]*maitrev1.FleetNodeStatus{}}
	if st := m.Status(nil); st[0].GetOnline() {
		t.Fatal("a peer unheard for longer than SilentFor is offline")
	}
	m.Received(&maitrev1.FleetNodeStatus{NodeId: "b"})
	if st := m.Status(nil); !st[0].GetOnline() {
		t.Fatal("gossip brings it back")
	}
}
