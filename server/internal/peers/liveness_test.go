package peers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// oldNode answers only the core: it predates every other call.
type oldNode struct {
	maitrev1connect.UnimplementedNodeServiceHandler
}

func (oldNode) GetNodeInfo(context.Context, *maitrev1.GetNodeInfoRequest) (*maitrev1.GetNodeInfoResponse, error) {
	return &maitrev1.GetNodeInfoResponse{Node: &maitrev1.NodeInfo{Name: "Avalon", Version: "95c3d8a"}}, nil
}

// ADR 0007: a peer is online when it answers GetNodeInfo. A call it does not
// know makes it need attention, never offline; a peer nobody can reach is
// offline whatever else happened.
func TestOnlineComesOnlyFromTheLivenessProbe(t *testing.T) {
	self, _ := identity.Generate()
	peerID, _ := identity.Generate()
	serverTrust := trust.New()
	serverTrust.Add(self.Fingerprint())
	rpc := connect.NewServer()
	maitrev1connect.RegisterNodeServiceHandler(rpc, oldNode{})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = mtls.ServerConfig(peerID, serverTrust)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	list := trust.New()
	list.Add(peerID.Fingerprint())
	m := New(nil, self, list, slog.Default())
	id := peerID.Fingerprint()
	m.peers[id] = &peer{id: id, name: "Avalon", addrs: []string{strings.TrimPrefix(srv.URL, "https://")}}
	status := func() *maitrev1.PeerStatus { return m.Status(context.Background())[0] }

	m.ProbeAll(context.Background())
	if st := status(); !st.GetOnline() || st.GetVersion() != "95c3d8a" || len(st.GetProblems()) != 0 {
		t.Fatalf("after the probe: %v", st)
	}

	// The old Node cannot take gossip: a problem, still online.
	m.Gossip(context.Background(), &maitrev1.FleetNodeStatus{NodeId: self.Fingerprint()})
	waitFor(t, func() bool { return len(status().GetProblems()) == 1 })
	if st := status(); !st.GetOnline() || st.GetProblems()[0] != "runs Maître Server 95c3d8a, which cannot answer PushStatus; update it" {
		t.Fatalf("after refused gossip: %v", st)
	}
	m.ProbeAll(context.Background())
	if !status().GetOnline() {
		t.Fatal("the next probe keeps it online")
	}

	// Updated: a new version clears what the old one could not do.
	m.Received(&maitrev1.FleetNodeStatus{NodeId: id, Version: "5803c23"})
	if st := status(); len(st.GetProblems()) != 0 || st.GetVersion() != "5803c23" {
		t.Fatalf("after the update: %v", st)
	}

	// Gone: a non-core call that reaches nobody changes nothing; the probe
	// says offline.
	srv.Close()
	_ = m.call(context.Background(), id, "PushRecords", func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
		_, err := c.PushRecords(ctx, &maitrev1.PushRecordsRequest{})
		return err
	})
	if st := status(); !st.GetOnline() || len(st.GetProblems()) != 0 {
		t.Fatalf("an unreachable push is not a problem and not the probe's call: %v", st)
	}
	m.ProbeAll(context.Background())
	if status().GetOnline() {
		t.Fatal("a peer that does not answer the probe is offline")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never held")
}
