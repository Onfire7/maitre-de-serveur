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
	"fmt"
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
	// When the Node record was made; a recent record never heard from is
	// a Node still being installed.
	provisioned time.Time
	// version is what the peer's last GetNodeInfo said; problems are the
	// calls it last answered with an error, by call name (ADR 0007).
	version  string
	problems map[string]string
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
	// installs is what each Node still being installed reported through
	// its Installer, memory only.
	installs map[string]*maitrev1.Provisioning
}

// New builds a Manager; list is the trust list shared with the listener.
func New(st *store.Store, id *identity.Identity, list *trust.List, log *slog.Logger) *Manager {
	cfg := mtls.ClientConfig(id, list)
	m := &Manager{st: st, id: id, list: list, log: log, peers: map[string]*peer{}, heard: map[string]*maitrev1.FleetNodeStatus{}, installs: map[string]*maitrev1.Provisioning{}}
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
		p.provisioned = time.UnixMilli(n.GetProvisionedUnixMs())
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

// SilentFor is how long a peer may go unheard (no answer to the liveness
// probe, no gossip) before it counts as offline (decision 162, ADR 0007).
var SilentFor = 60 * time.Second

// Status describes every peer for NodeInfo and GetFleetStatus. A peer is
// online when it answered the liveness probe or gossiped recently,
// whatever other calls said (ADR 0007): those only add problems.
func (m *Manager) Status(ctx context.Context) []*maitrev1.PeerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*maitrev1.PeerStatus
	for _, p := range m.peers {
		online := p.online && time.Since(p.lastSeen) < SilentFor
		st := &maitrev1.PeerStatus{NodeId: p.id, Name: p.name, Online: online, LastSeenUnixMs: p.lastSeen.UnixMilli(), Version: p.version}
		for _, msg := range p.problems {
			st.Problems = append(st.Problems, msg)
		}
		sort.Strings(st.Problems)
		if len(p.addrs) > 0 {
			st.Address = p.addrs[0]
		}
		if m.st != nil {
			if q, err := m.st.Pending(ctx, p.id); err == nil {
				st.Queued = uint32(len(q))
			}
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// call runs fn (the RPC named name) against a peer, trying its addresses
// in order until one answers. It never changes whether the peer is online
// (ADR 0007: only the liveness probe and gossip do): an error the peer sent
// back is recorded as a problem with that call, cleared by its next
// success, and a call that reached nobody leaves everything as it was.
func (m *Manager) call(ctx context.Context, id, name string, fn func(context.Context, maitrev1connect.NodeServiceClient) error) error {
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
		if err == nil || answered(err) {
			m.mu.Lock()
			p.addAddr(a, true)
			if err == nil {
				delete(p.problems, name)
			} else {
				if p.problems == nil {
					p.problems = map[string]string{}
				}
				p.problems[name] = problem(name, p.version, err)
			}
			m.mu.Unlock()
			return err
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return lastErr
}

// answered reports whether err came back from the peer itself, as opposed
// to the call never reaching it (dial, TLS, timeout).
func answered(err error) bool {
	var cerr *connect.Error
	return errors.As(err, &cerr) && cerr.IsRemote()
}

// problem words a call the peer refused for the Console.
func problem(name, version string, err error) string {
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		v := "an older Maître Server"
		if version != "" {
			v = "Maître Server " + version
		}
		return fmt.Sprintf("runs %s, which cannot answer %s; update it", v, name)
	}
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		return fmt.Sprintf("refused %s: %s", name, cerr.Message())
	}
	return fmt.Sprintf("refused %s: %v", name, err)
}

// ProbeEvery is how often every peer is asked GetNodeInfo, the call that
// decides whether it is online (ADR 0007).
var ProbeEvery = 15 * time.Second

// Probe asks every peer GetNodeInfo every ProbeEvery until ctx ends.
func (m *Manager) Probe(ctx context.Context) {
	t := time.NewTicker(ProbeEvery)
	defer t.Stop()
	for {
		m.ProbeAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ProbeAll asks every peer GetNodeInfo once, in parallel. A peer that
// answers, even with an error, is online and its version noted; one that
// cannot be reached on any address is offline.
func (m *Manager) ProbeAll(ctx context.Context) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.peers))
	for id := range m.peers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.probe(ctx, id)
		}()
	}
	wg.Wait()
}

func (m *Manager) probe(ctx context.Context, id string) {
	var version string
	err := m.call(ctx, id, "GetNodeInfo", func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
		res, err := c.GetNodeInfo(ctx, &maitrev1.GetNodeInfoRequest{})
		if err != nil {
			return err
		}
		version = res.GetNode().GetVersion()
		return nil
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.peers[id]
	if !ok {
		return
	}
	if err != nil && !answered(err) {
		p.online = false
		return
	}
	p.online, p.lastSeen = true, time.Now()
	if version != "" && version != p.version {
		// An updated Node starts with a clean slate.
		p.version, p.problems = version, nil
	}
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
			err := m.call(ctx, id, "PushRecords", func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
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
			err := m.call(ctx, id, "PushStatus", func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
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
// own calls to it succeed. A message without Service statuses (a plain
// metrics sample) keeps the statuses heard before.
func (m *Manager) Received(st *maitrev1.FleetNodeStatus) {
	if st == nil || st.GetNodeId() == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev := m.heard[st.GetNodeId()]; prev != nil && st.GetServicesUnixMs() == 0 {
		st.Services, st.ServicesUnixMs = prev.GetServices(), prev.GetServicesUnixMs()
	}
	m.heard[st.GetNodeId()] = st
	if p, ok := m.peers[st.GetNodeId()]; ok {
		p.online, p.lastSeen = true, time.Now()
		if v := st.GetVersion(); v != "" && v != p.version {
			p.version, p.problems = v, nil
		}
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
	ferr := m.call(ctx, owner, "PutRecord", func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
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

// InstallWindow is how long after its record is made a Node never heard
// from is taken to be still installing.
var InstallWindow = time.Hour

// WatchInstallsEvery is how often the Installers of Nodes still being
// installed are asked for their progress.
var WatchInstallsEvery = 5 * time.Second

// WatchInstalls asks the Installer of every Node still being installed for
// its progress until ctx ends, so any Node can tell a Console a peer is
// Provisioning. Such a Node has a record made within InstallWindow and has
// never gossiped; its Installer listens on the Node port at the address
// the Console put in the record, with the key that becomes the node_id.
func (m *Manager) WatchInstalls(ctx context.Context) {
	t := time.NewTicker(WatchInstallsEvery)
	defer t.Stop()
	for {
		m.pollInstalls(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Manager) pollInstalls(ctx context.Context) {
	type target struct {
		id    string
		addrs []string
	}
	var targets []target
	m.mu.Lock()
	for id, p := range m.peers {
		if m.heard[id] != nil || time.Since(p.provisioned) > InstallWindow {
			delete(m.installs, id)
			continue
		}
		targets = append(targets, target{id, append([]string(nil), p.addrs...)})
	}
	for id := range m.installs {
		if m.peers[id] == nil {
			delete(m.installs, id)
		}
	}
	m.mu.Unlock()
	for _, tg := range targets {
		for _, a := range tg.addrs {
			c := maitrev1connect.NewInstallerServiceClient(connect.NewClient(connecthttp.NewTransport(m.http, "https://"+a)))
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			res, err := c.GetInstallerStatus(cctx, &maitrev1.GetInstallerStatusRequest{})
			cancel()
			if err != nil {
				continue
			}
			// The TLS handshake pinned the key to the trust list, which
			// holds this Node's; check the Installer says the same.
			if res.GetStatus().GetInstallerId() != tg.id || res.GetStatus().GetProvisioning() == nil {
				continue
			}
			m.mu.Lock()
			if m.heard[tg.id] == nil {
				m.installs[tg.id] = res.GetStatus().GetProvisioning()
			}
			m.mu.Unlock()
			break
		}
	}
}

// Installing returns what the Installer of a Node still being installed
// last reported, or nil. A Node rebooting into its new system stops
// answering as an Installer; its last report (Rebooting) is kept until the
// Node gossips or InstallWindow passes.
func (m *Manager) Installing(id string) *maitrev1.Provisioning {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installs[id]
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
	if err := m.call(ctx, id, "ListRecords", func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
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
		// One Record that cannot be applied must not stop the rest, the
		// push back or the queued writes.
		ok, err := m.st.Apply(ctx, r)
		if err != nil {
			m.log.Warn("could not apply a peer's record", "peer", id, "kind", r.GetKind(), "id", r.GetId(), "err", err)
			continue
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
		_ = m.call(ctx, id, "PushRecords", func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
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
		err := m.call(ctx, id, "PutRecord", func(ctx context.Context, c maitrev1connect.NodeServiceClient) error {
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
