// Package peers is the multi-Node half of the sync layer (ADR 0003,
// Milestone 3). Every Node holds every Record; this keeps copies converging:
// pushes after each local write, a periodic reconcile that pulls a peer's
// whole (kilobyte-sized) database and applies it newest-wins, forwarding of
// writes to the Record's owner, and a queue for owners that are offline.
package peers

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// ReconcileEvery is how often each peer's database is pulled in full.
var ReconcileEvery = 20 * time.Second

type peer struct {
	id       string
	name     string
	addrs    []string // host:port, most recently working first
	online   bool
	lastSeen time.Time
	client   maitrev1connect.NodeServiceClient
}

// Manager tracks the Fleet's other Nodes and keeps the databases in sync.
type Manager struct {
	st   *store.Store
	id   *identity.Identity
	list *trust.List
	log  *slog.Logger
	// OnApplied runs after records from a peer changed the local database.
	OnApplied func()

	mu    sync.Mutex
	peers map[string]*peer
	http  *http.Client
	// heard is the last status each peer gossiped (ADR 0006), memory only.
	heard map[string]*maitrev1.FleetNodeStatus
}

// New builds a Manager; list is the trust list shared with the listener.
func New(st *store.Store, id *identity.Identity, list *trust.List, log *slog.Logger) *Manager {
	cfg := mtls.ClientConfig(id, list)
	m := &Manager{st: st, id: id, list: list, log: log, peers: map[string]*peer{}, heard: map[string]*maitrev1.FleetNodeStatus{}}
	m.http = &http.Client{Transport: &http2.Transport{
		TLSClientConfig: cfg,
		DialTLSContext: func(ctx context.Context, network, addr string, c *tls.Config) (net.Conn, error) {
			d := &tls.Dialer{Config: c, NetDialer: &net.Dialer{Timeout: 5 * time.Second}}
			return d.DialContext(ctx, network, addr)
		},
	}}
	return m
}

// Refresh reloads the peer set from the Node records. Addresses already
// learned (from mDNS or a successful call) are kept in front.
func (m *Manager) Refresh(ctx context.Context) error {
	nodes, err := store.ListLive(ctx, m.st, store.KindNode, func() *maitrev1.Node { return &maitrev1.Node{} })
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.GetNodeId() == m.id.Fingerprint() {
			continue
		}
		seen[n.GetNodeId()] = true
		p, ok := m.peers[n.GetNodeId()]
		if !ok {
			p = &peer{id: n.GetNodeId()}
			m.peers[p.id] = p
		}
		p.name = n.GetName()
		for _, a := range n.GetAddresses() {
			p.addAddr(a, false)
		}
	}
	for id := range m.peers {
		if !seen[id] {
			delete(m.peers, id)
		}
	}
	return nil
}

func (p *peer) addAddr(a string, front bool) {
	for i, x := range p.addrs {
		if x == a {
			if front && i > 0 {
				p.addrs = append([]string{a}, append(p.addrs[:i:i], p.addrs[i+1:]...)...)
			}
			return
		}
	}
	if front {
		p.addrs = append([]string{a}, p.addrs...)
	} else {
		p.addrs = append(p.addrs, a)
	}
	p.client = nil
}

// Discovered records an address mDNS reported for a peer.
func (m *Manager) Discovered(id, addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.peers[id]; ok {
		p.addAddr(addr, true)
	}
}

// HTTPClient is the mTLS client this Node uses to reach peers; other
// packages (the file manager) reuse it for their own peer calls.
func (m *Manager) HTTPClient() *http.Client { return m.http }

// Addresses returns every address known per peer, best first.
func (m *Manager) Addresses() map[string][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]string{}
	for id, p := range m.peers {
		out[id] = append([]string(nil), p.addrs...)
	}
	return out
}

// Status describes every peer for NodeInfo.
func (m *Manager) Status(ctx context.Context) []*maitrev1.PeerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*maitrev1.PeerStatus
	for _, p := range m.peers {
		st := &maitrev1.PeerStatus{NodeId: p.id, Name: p.name, Online: p.online, LastSeenUnixMs: p.lastSeen.UnixMilli()}
		if len(p.addrs) > 0 {
			st.Address = p.addrs[0]
		}
		if q, err := m.st.Pending(ctx, p.id); err == nil {
			st.Queued = uint32(len(q))
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// call runs fn against a peer, trying its addresses in order until one
// answers; a success moves that address to the front and marks it online.
func (m *Manager) call(ctx context.Context, id string, fn func(context.Context, maitrev1connect.NodeServiceClient) error) error {
	m.mu.Lock()
	p, ok := m.peers[id]
	if !ok {
		m.mu.Unlock()
		return errors.New("peers: unknown node " + id)
	}
	addrs := append([]string(nil), p.addrs...)
	m.mu.Unlock()
	if len(addrs) == 0 {
		return errors.New("peers: no address known for " + p.name)
	}
	var lastErr error
	for _, a := range addrs {
		c := maitrev1connect.NewNodeServiceClient(connect.NewClient(connecthttp.NewTransport(m.http, "https://"+a)))
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := fn(cctx, c)
		cancel()
		m.mu.Lock()
		if err == nil {
			p.addAddr(a, true)
			p.online, p.lastSeen = true, time.Now()
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	m.mu.Lock()
	p.online = false
	m.mu.Unlock()
	return lastErr
}

// Push sends records to every peer; failures are left to the next reconcile.
func (m *Manager) Push(ctx context.Context, recs ...*maitrev1.Record) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.peers))
	for id := range m.peers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		id := id
		go func() {
			err := m.call(ctx, id, func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
				_, err := c.PushRecords(ctx, &maitrev1.PushRecordsRequest{Records: recs})
				return err
			})
			if err != nil {
				m.log.Debug("push failed; reconcile will catch up", "peer", id, "err", err)
			}
		}()
	}
}

// Gossip hands this Node's own status to every peer (ADR 0006). Failures
// are dropped: the next sample comes in a few seconds.
func (m *Manager) Gossip(ctx context.Context, st *maitrev1.FleetNodeStatus) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.peers))
	for id := range m.peers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		id := id
		go func() {
			err := m.call(ctx, id, func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
				_, err := c.PushStatus(ctx, &maitrev1.PushStatusRequest{Status: st})
				return err
			})
			if err != nil {
				m.log.Debug("gossip failed", "peer", id, "err", err)
			}
		}()
	}
}

// Received keeps a peer's gossiped status and counts it as having been
// heard from: a Node that talks to us is reachable, whether or not our
// own calls to it succeed.
func (m *Manager) Received(st *maitrev1.FleetNodeStatus) {
	if st == nil || st.GetNodeId() == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heard[st.GetNodeId()] = st
	if p, ok := m.peers[st.GetNodeId()]; ok {
		p.online, p.lastSeen = true, time.Now()
	}
}

// Last returns the last status a peer gossiped, or nil.
func (m *Manager) Last(id string) *maitrev1.FleetNodeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.heard[id]
}

// Forward writes a record owned by another Node by asking that Node; when
// it cannot be reached the write is queued and queued=true is returned.
func (m *Manager) Forward(ctx context.Context, owner string, rec *maitrev1.Record) (applied *maitrev1.Record, queued bool, err error) {
	ferr := m.call(ctx, owner, func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
		res, err := c.PutRecord(ctx, &maitrev1.PutRecordRequest{Record: rec})
		if err != nil {
			return err
		}
		applied = res.GetRecord()
		return nil
	})
	if ferr == nil {
		return applied, false, nil
	}
	var cerr *connect.Error
	if errors.As(ferr, &cerr) && cerr.Code() != connect.CodeUnavailable && cerr.Code() != connect.CodeDeadlineExceeded {
		// The owner answered and refused: that is the caller's problem, not a queue's.
		return nil, false, ferr
	}
	if err := m.st.Enqueue(ctx, owner, rec.GetKind(), rec.GetId(), rec.GetBody(), rec.GetDeleted()); err != nil {
		return nil, false, err
	}
	m.log.Info("write queued for offline owner", "owner", owner, "kind", rec.GetKind(), "id", rec.GetId())
	return nil, true, nil
}

// Run reconciles with every peer periodically until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(ReconcileEvery)
	defer t.Stop()
	for {
		m.ReconcileAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ReconcileAll pulls every peer's records, applies what is newer, pushes
// what we hold that they lack, and delivers queued writes to owners back
// online.
func (m *Manager) ReconcileAll(ctx context.Context) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.peers))
	for id := range m.peers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		if err := m.reconcile(ctx, id); err != nil {
			m.log.Debug("reconcile", "peer", id, "err", err)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context, id string) error {
	var theirs []*maitrev1.Record
	if err := m.call(ctx, id, func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
		res, err := c.ListRecords(ctx, &maitrev1.ListRecordsRequest{})
		if err != nil {
			return err
		}
		theirs = res.GetRecords()
		return nil
	}); err != nil {
		return err
	}
	changed := false
	index := map[string]*maitrev1.Record{}
	for _, r := range theirs {
		index[r.GetKind()+"\x00"+r.GetId()] = r
		ok, err := m.st.Apply(ctx, r)
		if err != nil {
			return err
		}
		changed = changed || ok
	}
	// Anything we hold that they lack or hold older goes back to them.
	ours, err := m.st.List(ctx, "")
	if err != nil {
		return err
	}
	var missing []*maitrev1.Record
	for _, r := range ours {
		t, ok := index[r.GetKind()+"\x00"+r.GetId()]
		if !ok || !proto.Equal(t, r) && newer(r, t) {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		_ = m.call(ctx, id, func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
			_, err := c.PushRecords(ctx, &maitrev1.PushRecordsRequest{Records: missing})
			return err
		})
	}
	// Queued writes for this owner.
	pending, err := m.st.Pending(ctx, id)
	if err != nil {
		return err
	}
	for _, q := range pending {
		rec := &maitrev1.Record{Kind: q.Kind, Id: q.ID, Body: q.Body, Deleted: q.Deleted}
		err := m.call(ctx, id, func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
			_, err := c.PutRecord(ctx, &maitrev1.PutRecordRequest{Record: rec})
			return err
		})
		if err != nil {
			var cerr *connect.Error
			if errors.As(err, &cerr) && cerr.Code() != connect.CodeUnavailable {
				m.log.Warn("queued write refused by owner; dropping it", "owner", id, "kind", q.Kind, "id", q.ID, "err", err)
				_ = m.st.Dequeue(ctx, q.Seq)
			}
			break
		}
		_ = m.st.Dequeue(ctx, q.Seq)
		m.log.Info("queued write delivered", "owner", id, "kind", q.Kind, "id", q.ID)
	}
	if changed && m.OnApplied != nil {
		m.OnApplied()
	}
	return nil
}

// newer mirrors store.Apply's rule: same owner, higher version; or another
// owner with a later timestamp (ties by owner id).
func newer(a, b *maitrev1.Record) bool {
	if a.GetOwnerNodeId() == b.GetOwnerNodeId() {
		return a.GetVersion() > b.GetVersion()
	}
	return a.GetUpdatedUnixMs() > b.GetUpdatedUnixMs() || (a.GetUpdatedUnixMs() == b.GetUpdatedUnixMs() && a.GetOwnerNodeId() > b.GetOwnerNodeId())
}

// TrustedFingerprints is every key a Node should accept in full: Trusted
// Devices that are not recovery-only, plus every other Node of the Fleet.
// Recovery-only keys come back separately (decision 100).
func TrustedFingerprints(ctx context.Context, st *store.Store, self string) (full, limited []string, err error) {
	var fps []string
	devices, err := store.ListLive(ctx, st, store.KindTrustedDevice, func() *maitrev1.TrustedDevice { return &maitrev1.TrustedDevice{} })
	if err != nil {
		return nil, nil, err
	}
	for _, d := range devices {
		switch {
		case d.GetNodeScope() != "" && d.GetNodeScope() != self:
			// Decision 120: trusted by another Node only, not by us.
		case d.GetRecoveryOnly():
			limited = append(limited, d.GetDeviceId())
		default:
			fps = append(fps, d.GetDeviceId())
		}
	}
	nodes, err := store.ListLive(ctx, st, store.KindNode, func() *maitrev1.Node { return &maitrev1.Node{} })
	if err != nil {
		return nil, nil, err
	}
	for _, n := range nodes {
		if n.GetNodeId() != self {
			fps = append(fps, n.GetNodeId())
		}
	}
	return fps, limited, nil
}
