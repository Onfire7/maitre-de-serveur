package peers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/nodeservice"
	caller "github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// refusing is a Node whose database will not take pushed records, like
// one from before tombstones could be applied.
type refusing struct {
	*nodeservice.Service
	refuse bool
}

func (r *refusing) PushRecords(ctx context.Context, req *maitrev1.PushRecordsRequest) (*maitrev1.PushRecordsResponse, error) {
	if r.refuse {
		return nil, connect.NewError(connect.CodeInternal, "NOT NULL constraint failed: records.body")
	}
	return r.Service.PushRecords(ctx, req)
}

// Decision 168: records a peer cannot take are reported once they are still
// missing a sync later, and the report goes when they arrive.
func TestSyncGapsNeedTwoSyncs(t *testing.T) {
	ctx := context.Background()
	selfID, _ := identity.Generate()
	peerID, _ := identity.Generate()
	open := func(id *identity.Identity) *store.Store {
		st, err := store.Open(filepath.Join(t.TempDir(), "fleet.db"), id.Fingerprint())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
	ours, theirs := open(selfID), open(peerID)
	b := &refusing{Service: nodeservice.New(peerID, nodeservice.Info{}, theirs), refuse: true}
	serverTrust := trust.New()
	serverTrust.Add(selfID.Fingerprint())
	rpc := connect.NewServer()
	maitrev1connect.RegisterNodeServiceHandler(rpc, b)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)
	srv := httptest.NewUnstartedServer(caller.Middleware(mux))
	srv.TLS = mtls.ServerConfig(peerID, serverTrust)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	list := trust.New()
	list.Add(peerID.Fingerprint())
	m := New(ours, selfID, list, slog.Default())
	m.peers[peerID.Fingerprint()] = &peer{id: peerID.Fingerprint(), name: "Boulder", addrs: []string{strings.TrimPrefix(srv.URL, "https://")}}
	if _, err := ours.Put(ctx, "service", "jellyfin", &maitrev1.ServiceSpec{Name: "jellyfin"}, false); err != nil {
		t.Fatal(err)
	}

	sync := func() {
		t.Helper()
		if err := m.reconcile(ctx, peerID.Fingerprint()); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	if g := m.Gaps(); len(g) != 0 {
		t.Fatalf("one sync is not enough to call it out of sync: %v", g)
	}
	sync()
	g := m.Gaps()
	if len(g) != 1 || g[0].GetNodeId() != peerID.Fingerprint() || g[0].GetAheadNodeId() != selfID.Fingerprint() || g[0].GetCount() != 1 || g[0].GetRecords()[0] != "service jellyfin" {
		t.Fatalf("Boulder lacks our jellyfin: %v", g)
	}

	b.refuse = false
	sync() // pushed and taken now
	sync()
	if g := m.Gaps(); len(g) != 0 {
		t.Fatalf("in sync again: %v", g)
	}
}

// The other direction: what we could not take of the peer's records.
func TestSyncGapsWeLack(t *testing.T) {
	self, _ := identity.Generate()
	m := New(nil, self, trust.New(), slog.Default())
	m.peers["b"] = &peer{id: "b", name: "Boulder"}
	rec := []*maitrev1.Record{{Kind: "service", Id: "uptime-kuma"}}
	m.noteGaps("b", rec, nil)
	m.noteGaps("b", rec, nil)
	g := m.Gaps()
	if len(g) != 1 || g[0].GetNodeId() != self.Fingerprint() || g[0].GetAheadNodeId() != "b" {
		t.Fatalf("we are behind Boulder: %v", g)
	}
	m.noteGaps("b", nil, nil)
	if len(m.Gaps()) != 0 {
		t.Fatal("cleared once we have it")
	}
}
