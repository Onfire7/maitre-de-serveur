package nodeservice

import (
	"context"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
)

// fakePeers is the sync layer as GetFleetStatus sees it: two peers, one of
// which has gossiped.
type fakePeers struct {
	heard map[string]*maitrev1.FleetNodeStatus
}

func (f *fakePeers) Forward(context.Context, string, *maitrev1.Record) (*maitrev1.Record, bool, error) {
	return nil, false, nil
}
func (f *fakePeers) Push(context.Context, ...*maitrev1.Record) {}
func (f *fakePeers) Status(context.Context) []*maitrev1.PeerStatus {
	return []*maitrev1.PeerStatus{
		{NodeId: "b", Name: "Basalt", Online: true, LastSeenUnixMs: 1000},
		{NodeId: "c", Name: "Cedar", Online: false, LastSeenUnixMs: 500},
	}
}
func (f *fakePeers) Received(st *maitrev1.FleetNodeStatus)    { f.heard[st.GetNodeId()] = st }
func (f *fakePeers) Last(id string) *maitrev1.FleetNodeStatus { return f.heard[id] }

func TestGetFleetStatusMergesGossip(t *testing.T) {
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	s := New(id, Info{Name: "Alder"}, nil)
	peers := &fakePeers{heard: map[string]*maitrev1.FleetNodeStatus{}}
	s.Peers = peers
	s.Metrics = func() *maitrev1.NodeMetrics { return &maitrev1.NodeMetrics{CpuPercent: 12} }
	s.Services = func(context.Context) []*maitrev1.ServiceStatus {
		return []*maitrev1.ServiceStatus{{Spec: &maitrev1.ServiceSpec{Name: "jellyfin"}, State: maitrev1.ServiceStatus_STATE_RUNNING, Healthy: true}}
	}

	// Basalt gossips; Cedar never does.
	if _, err := s.PushStatus(context.Background(), &maitrev1.PushStatusRequest{Status: &maitrev1.FleetNodeStatus{
		NodeId: "b", Metrics: &maitrev1.NodeMetrics{CpuPercent: 60}, MetricsUnixMs: 900,
		Services: []*maitrev1.ServiceStatus{{Spec: &maitrev1.ServiceSpec{Name: "uptime"}, State: maitrev1.ServiceStatus_STATE_STARTING}}, ServicesUnixMs: 800,
	}}); err != nil {
		t.Fatal(err)
	}

	res, err := s.GetFleetStatus(context.Background(), &maitrev1.GetFleetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetNodeId() != id.Fingerprint() || len(res.Nodes) != 3 || res.Nodes[0].GetNodeId() != id.Fingerprint() {
		t.Fatalf("self first and every peer expected, got %v", res.Nodes)
	}
	self := res.Nodes[0]
	if !self.GetReachable() || self.GetMetrics().GetCpuPercent() != 12 || len(self.GetServices()) != 1 || self.GetServices()[0].GetSpec().GetName() != "jellyfin" {
		t.Fatalf("own status wrong: %v", self)
	}
	byID := map[string]*maitrev1.FleetNodeStatus{}
	for _, n := range res.Nodes {
		byID[n.GetNodeId()] = n
	}
	b := byID["b"]
	if !b.GetReachable() || b.GetLastSeenUnixMs() != 1000 || b.GetMetrics().GetCpuPercent() != 60 || b.GetMetricsUnixMs() != 900 || len(b.GetServices()) != 1 || b.GetServices()[0].GetSpec().GetName() != "uptime" {
		t.Fatalf("Basalt's gossip not merged: %v", b)
	}
	c := byID["c"]
	if c.GetReachable() || c.GetMetrics() != nil || len(c.GetServices()) != 0 || c.GetLastSeenUnixMs() != 500 {
		t.Fatalf("Cedar should be unreachable with nothing heard: %v", c)
	}
}

func TestPushStatusIgnoresEmpty(t *testing.T) {
	id, _ := identity.Generate()
	s := New(id, Info{}, nil)
	peers := &fakePeers{heard: map[string]*maitrev1.FleetNodeStatus{}}
	s.Peers = peers
	if _, err := s.PushStatus(context.Background(), &maitrev1.PushStatusRequest{}); err != nil {
		t.Fatal(err)
	}
	// A nil status reaches Received; the real Manager drops it, the fake stores under "".
	if len(peers.heard) > 1 {
		t.Fatalf("unexpected: %v", peers.heard)
	}
}
