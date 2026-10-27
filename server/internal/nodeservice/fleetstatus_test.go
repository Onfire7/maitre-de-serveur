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
	heard    map[string]*maitrev1.FleetNodeStatus
	installs map[string]*maitrev1.Provisioning
}

func (f *fakePeers) Forward(context.Context, string, *maitrev1.Record) (*maitrev1.Record, bool, error) {
	return nil, false, nil
}
func (f *fakePeers) Push(context.Context, ...*maitrev1.Record) {}
func (f *fakePeers) Status(context.Context) []*maitrev1.PeerStatus {
	return []*maitrev1.PeerStatus{
		{NodeId: "b", Name: "Basalt", Online: true, LastSeenUnixMs: 1000, Version: "95c3d8a", Problems: []string{"runs Maître Server 95c3d8a, which cannot answer PushStatus; update it"}},
		{NodeId: "c", Name: "Cedar", Online: false, LastSeenUnixMs: 500},
	}
}
func (f *fakePeers) Received(st *maitrev1.FleetNodeStatus)    { f.heard[st.GetNodeId()] = st }
func (f *fakePeers) Last(id string) *maitrev1.FleetNodeStatus { return f.heard[id] }
func (f *fakePeers) Gaps() []*maitrev1.SyncGap {
	return []*maitrev1.SyncGap{{NodeId: "b", AheadNodeId: "self", Count: 1, Records: []string{"service jellyfin"}}}
}
func (f *fakePeers) Installing(id string) *maitrev1.Provisioning {
	return f.installs[id]
}

func TestGetFleetStatusMergesGossip(t *testing.T) {
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	s := New(id, Info{Name: "Alder", Version: "5803c23"}, nil)
	// Cedar is still being installed.
	peers := &fakePeers{heard: map[string]*maitrev1.FleetNodeStatus{}, installs: map[string]*maitrev1.Provisioning{
		"c": {Phase: maitrev1.Provisioning_PHASE_INSTALLING, Percent: 40},
	}}
	s.Peers = peers
	s.Metrics = func() *maitrev1.NodeMetrics { return &maitrev1.NodeMetrics{CpuPercent: 12} }
	s.Services = func(context.Context) []*maitrev1.ServiceStatus {
		return []*maitrev1.ServiceStatus{{Spec: &maitrev1.ServiceSpec{Name: "jellyfin"}, State: maitrev1.ServiceStatus_STATE_RUNNING, Healthy: true}}
	}

	// Basalt gossips; Cedar never does.
	if _, err := s.PushStatus(context.Background(), &maitrev1.PushStatusRequest{Status: &maitrev1.FleetNodeStatus{
		NodeId: "b", Metrics: &maitrev1.NodeMetrics{CpuPercent: 60}, MetricsUnixMs: 900,
		Services: []*maitrev1.ServiceStatus{{Spec: &maitrev1.ServiceSpec{Name: "uptime"}, State: maitrev1.ServiceStatus_STATE_STARTING}}, ServicesUnixMs: 800,
		UpdateState: maitrev1.UpdateStatus_STATE_UPDATING, UpdateMessage: "pulling", UpdateProgress: 0.5, UpdateStaged: true, LastRollback: "health check failed", UpdateAvailable: true,
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
	if b.GetUpdateState() != maitrev1.UpdateStatus_STATE_UPDATING || b.GetUpdateMessage() != "pulling" || b.GetUpdateProgress() != 0.5 || !b.GetUpdateStaged() || b.GetLastRollback() != "health check failed" || !b.GetUpdateAvailable() || b.GetProvisioning() != nil {
		t.Fatalf("Basalt's update state not merged: %v", b)
	}
	if len(self.GetSyncGaps()) != 1 || self.GetSyncGaps()[0].GetRecords()[0] != "service jellyfin" {
		t.Fatalf("own sync gaps not reported: %v", self.GetSyncGaps())
	}
	if self.GetVersion() != "5803c23" || b.GetVersion() != "95c3d8a" || len(b.GetProblems()) != 1 {
		t.Fatalf("versions and problems not reported: self %v, Basalt %v", self, b)
	}
	c := byID["c"]
	if c.GetReachable() || c.GetMetrics() != nil || len(c.GetServices()) != 0 || c.GetLastSeenUnixMs() != 500 {
		t.Fatalf("Cedar should be unreachable with nothing heard: %v", c)
	}
	if c.GetProvisioning().GetPercent() != 40 {
		t.Fatalf("Cedar's install progress not reported: %v", c)
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
