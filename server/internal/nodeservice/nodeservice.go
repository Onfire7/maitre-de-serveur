// Package nodeservice implements maitre.v1.NodeService.
package nodeservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/authz"
	"github.com/Onfire7/maitre-de-serveur/server/internal/debugsurface"
	"github.com/Onfire7/maitre-de-serveur/server/internal/depots"
	"github.com/Onfire7/maitre-de-serveur/server/internal/history"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/parity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/rollout"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/timezone"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
)

// Info is the static part of what GetNodeInfo reports.
type Info struct {
	Name    string
	FleetID string
	Version string
	DevMode bool
	// DebugBuild is decision 323: this image carries the debug console's
	// remote surface, which the Console advertises in amber.
	DebugBuild bool
}

// Service answers NodeService calls.
type Service struct {
	id      *identity.Identity
	info    Info
	store   *store.Store // nil in dev mode without a database
	started time.Time
	// OnChange is called after every applied write so the agent can refresh
	// derived state such as the trust list. Optional.
	OnChange func(*maitrev1.Record)
	// Entries are the entry roles this Node holds (decision 218), gossiped.
	Entries func() []*maitrev1.EntryHold
	// Parity is this Node's parity status and Drains its running Drain
	// jobs (ADR 0009), gossiped; nil when the Node has neither.
	Parity func(ctx context.Context) *maitrev1.ParityStatus
	Drains func() []*maitrev1.DrainJob
	// Copies and Mirrors are each Space's copies and each Mirrored
	// Drive's health here (ADR 0009), gossiped.
	Copies  func() []*maitrev1.CopiesStatus
	Mirrors func(ctx context.Context) []*maitrev1.MirrorStatus
	// Metrics supplies GetNodeMetrics; nil means empty readings.
	Metrics func() *maitrev1.NodeMetrics
	// History holds the long-term metrics (decision 210); nil answers
	// Unimplemented, as an older Node does.
	History *history.Store
	// Services lists this Node's Services with live status for
	// GetFleetStatus; nil means none.
	Services func(ctx context.Context) []*maitrev1.ServiceStatus
	// The last update status read for the gossip, so `bootc status` runs
	// at most every 30 s when idle (every 5 s while an update runs).
	updMu   sync.Mutex
	updLast *maitrev1.UpdateStatus
	updAt   time.Time
	// Updates drives bootc; nil means this Node cannot update itself (dev mode).
	Updates *update.Manager
	// Peers forwards writes to their owner, pushes ours, and reports the
	// Fleet's other Nodes. Nil on a single-Node or dev setup.
	Peers Peers
	// Rename gives the Node a new name (record, host name, announcement);
	// nil in dev mode.
	Rename func(ctx context.Context, name string) (*maitrev1.Node, error)
	// IsNode reports whether a caller key is one of the Fleet's Nodes,
	// which the debug tier refuses: nothing in the Fleet protocol needs
	// it, so a Node asking is a Node that has been taken over.
	IsNode func(fingerprint string) bool
	// Limited reports whether a caller key is recovery-only (decision 100):
	// such a caller may only add a Trusted Device.
	Limited func(fingerprint string) bool
	// Authz checks the signatures of changes (decision 189); nil accepts
	// unsigned ones (dev mode, tests).
	Authz *authz.Policy
	// Tailscale reports the Node's tsnet state; nil when not joined.
	Tailscale func() *maitrev1.TailscaleStatus
	// Rollout reports the staged rollout's last decision; nil if none.
	Rollout func() string
	// Runs reports this Node's finished update runs from its own record,
	// newest last; nil when it keeps none. Reported so the history can be
	// read at all: the record held it and nothing ever showed it.
	Runs func() []*maitrev1.UpdateRun
	// Available is the newer image digest the registry last offered; nil
	// if none (UpdateStatus.available).
	Available func() string
	// Offers is what the Depots this Node reaches list, newest first
	// (UpdateStatus.depot_images); nil when the Node asks none.
	Offers func() []*maitrev1.DepotImage
	// PokeDepots asks for a fresh Depot listing soon, without waiting; a
	// Console with the update dialog open polls GetUpdateStatus.
	PokeDepots func()
	// PublicIP reports the address the internet sees this Node from
	// (decision 112); nil or "" when unknown.
	PublicIP func() string
	// PortMapping describes the UPnP router this Node can ask for port
	// forwarding (decision 115); nil or "" when unknown.
	PortMapping func() string
}

// Peers is what the sync layer offers this service (implemented by
// peers.Manager; an interface so the service tests need no network).
type Peers interface {
	Forward(ctx context.Context, owner string, rec *maitrev1.Record) (*maitrev1.Record, bool, error)
	Push(ctx context.Context, recs ...*maitrev1.Record)
	Status(ctx context.Context) []*maitrev1.PeerStatus
	// Received keeps a peer's gossiped status; Last returns it (ADR 0006).
	Received(st *maitrev1.FleetNodeStatus)
	Last(id string) *maitrev1.FleetNodeStatus
	// Installing is what a Node still being installed last reported
	// through its Installer, or nil.
	Installing(id string) *maitrev1.Provisioning
	// Gaps is what this Node found out of sync with its peers (decision 168).
	Gaps() []*maitrev1.SyncGap
}

var _ maitrev1connect.NodeServiceHandler = (*Service)(nil)

// New builds a Service for the given identity. st may be nil.
func New(id *identity.Identity, info Info, st *store.Store) *Service {
	return &Service{id: id, info: info, store: st, started: time.Now()}
}

// GetNodeInfo implements NodeService.
func (s *Service) GetNodeInfo(ctx context.Context, _ *maitrev1.GetNodeInfoRequest) (*maitrev1.GetNodeInfoResponse, error) {
	info := &maitrev1.NodeInfo{
		NodeId:        s.id.Fingerprint(),
		Name:          s.info.Name,
		FleetId:       s.info.FleetID,
		Version:       s.info.Version,
		UptimeSeconds: uint64(time.Since(s.started).Seconds()),
		DevMode:       s.info.DevMode,
		DebugBuild:    s.info.DebugBuild,
		// Capabilities the Console checks before offering a feature (plan C5).
		Features: []string{"components", "setup"},
	}
	if s.Peers != nil {
		info.Peers = s.Peers.Status(ctx)
	}
	if s.Tailscale != nil {
		info.Tailscale = s.Tailscale()
	}
	if s.PublicIP != nil {
		info.PublicIp = s.PublicIP()
	}
	if s.PortMapping != nil {
		info.PortMapping = s.PortMapping()
	}
	return &maitrev1.GetNodeInfoResponse{Node: info}, nil
}

// ListRecords implements NodeService.
func (s *Service) ListRecords(ctx context.Context, req *maitrev1.ListRecordsRequest) (*maitrev1.ListRecordsResponse, error) {
	if s.store == nil {
		return &maitrev1.ListRecordsResponse{}, nil
	}
	recs, err := s.store.List(ctx, req.GetKind())
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	return &maitrev1.ListRecordsResponse{Records: recs}, nil
}

// FleetWide reports whether a kind is owned by whichever Node last wrote it.
func FleetWide(kind string) bool {
	switch kind {
	case store.KindFleet, store.KindTrustedDevice, "space", "tailscale", "update_policy", "public_settings", "fleet_domain", timezone.Kind, authz.KindGrant, "catalogs":
		return true
	}
	return false
}

// PutRecord implements NodeService. A Record this Node owns (or a new one)
// is written here; a Fleet-wide kind takes ownership newest-wins; anything
// owned by another Node is forwarded to it, or queued while it is offline
// (ADR 0003). A Node record for a Node other than the caller is an
// introduction (store.Introduce).
func (s *Service) PutRecord(ctx context.Context, req *maitrev1.PutRecordRequest) (*maitrev1.PutRecordResponse, error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node has no Fleet database (dev mode)")
	}
	in := req.GetRecord()
	if in.GetKind() == "" || in.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "record needs kind and id")
	}
	// Decision 189: a change must be signed by a Trusted Device (or the
	// Recovery Phrase, for a new device), whoever delivers it: the Console
	// itself, or a Node forwarding it to the Record's owner.
	if s.Authz != nil {
		if _, err := s.Authz.AdmitFromDevice(ctx, in); err != nil {
			return nil, connect.NewError(connect.CodePermissionDenied, err.Error())
		}
	}
	if p, ok := peer.From(ctx); ok && s.Limited != nil && s.Limited(p.Fingerprint) {
		if in.GetKind() != store.KindTrustedDevice || in.GetDeleted() {
			return nil, connect.NewError(connect.CodePermissionDenied, "the Recovery Phrase may only add a Trusted Device")
		}
		var d maitrev1.TrustedDevice
		if err := proto.Unmarshal(in.GetBody(), &d); err != nil || d.GetRecoveryOnly() || d.GetPublicKeyPem() == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a full (not recovery-only) Trusted Device with a key is required")
		}
	}
	if in.GetKind() == "space" && !in.GetDeleted() {
		var b maitrev1.Space
		if err := proto.Unmarshal(in.GetBody(), &b); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "bad Space body")
		}
		if b.GetSpaceId() != in.GetId() {
			return nil, connect.NewError(connect.CodeInvalidArgument, "the Space's id must match the record id")
		}
		if err := storage.ValidateSpace(&b); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
	}
	if in.GetKind() == parity.KindSchedule && !in.GetDeleted() {
		var sch maitrev1.ProtectionSchedule
		if err := proto.Unmarshal(in.GetBody(), &sch); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "bad protection schedule")
		}
		if err := parity.ValidateSchedule(&sch); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
	}
	if in.GetKind() == store.KindNode && in.GetId() != s.id.Fingerprint() && in.GetDeleted() {
		// Removing a Node from the Fleet (guard on the Console side): a
		// tombstone in the Node's own name, so it wins everywhere.
		rec, err := s.store.Tombstone(ctx, store.KindNode, in.GetId(), in)
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, "no such Node")
		} else if err != nil {
			return nil, connect.Errorf(connect.CodeInternal, "%v", err)
		}
		if s.OnChange != nil {
			s.OnChange(rec)
		}
		if s.Peers != nil {
			s.Peers.Push(context.WithoutCancel(ctx), rec)
		}
		return &maitrev1.PutRecordResponse{Record: rec}, nil
	}
	if in.GetKind() == store.KindNode && in.GetId() != s.id.Fingerprint() && !in.GetDeleted() {
		var n maitrev1.Node
		if err := proto.Unmarshal(in.GetBody(), &n); err != nil || n.GetNodeId() != in.GetId() || n.GetPublicKeyPem() == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "node record must carry node_id and public_key_pem")
		}
		added, err := s.store.IntroduceSigned(ctx, in)
		if err != nil {
			return nil, connect.Errorf(connect.CodeInternal, "%v", err)
		}
		rec, err := s.store.Get(ctx, store.KindNode, in.GetId(), &maitrev1.Node{})
		if err != nil {
			return nil, connect.Errorf(connect.CodeInternal, "node %s after introduction: %v", in.GetId()[:min(8, len(in.GetId()))], err)
		}
		if added {
			if s.OnChange != nil {
				s.OnChange(rec)
			}
			if s.Peers != nil {
				s.Peers.Push(context.WithoutCancel(ctx), rec)
			}
		}
		return &maitrev1.PutRecordResponse{Record: rec}, nil
	}
	fleetWide := FleetWide(in.GetKind())
	if !fleetWide && s.Peers != nil {
		owner, err := s.store.Owner(ctx, in.GetKind(), in.GetId())
		if err != nil {
			return nil, connect.Errorf(connect.CodeInternal, "%v", err)
		}
		if in.GetDeleted() && owner != "" && owner != s.id.Fingerprint() && s.removedNode(ctx, owner) {
			// What a removed Node left behind (decision 276): its owner
			// will never take the write, so the tombstone is written here
			// in the owner's name, as a Node's removal is.
			rec, err := s.store.Tombstone(ctx, in.GetKind(), in.GetId(), in)
			if err != nil {
				return nil, connect.Errorf(connect.CodeInternal, "%v", err)
			}
			if s.OnChange != nil {
				s.OnChange(rec)
			}
			s.Peers.Push(context.WithoutCancel(ctx), rec)
			return &maitrev1.PutRecordResponse{Record: rec}, nil
		}
		if owner != "" && owner != s.id.Fingerprint() {
			applied, queued, err := s.Peers.Forward(ctx, owner, in)
			if err != nil {
				return nil, err
			}
			if queued {
				return &maitrev1.PutRecordResponse{Queued: true}, nil
			}
			if applied != nil {
				_, _ = s.store.Apply(ctx, applied)
			}
			return &maitrev1.PutRecordResponse{Record: applied}, nil
		}
	}
	var (
		rec *maitrev1.Record
		err error
	)
	switch {
	case len(in.GetSignature()) > 0:
		rec, err = s.store.PutSigned(ctx, in, fleetWide)
	case in.GetDeleted():
		rec, err = s.store.Delete(ctx, in.GetKind(), in.GetId(), fleetWide)
	default:
		rec, err = s.store.PutRaw(ctx, in.GetKind(), in.GetId(), in.GetBody(), fleetWide)
	}
	switch {
	case errors.Is(err, store.ErrStale):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	case errors.Is(err, store.ErrNotOwner):
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	case err != nil:
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	if s.OnChange != nil {
		s.OnChange(rec)
	}
	if s.Peers != nil {
		s.Peers.Push(context.WithoutCancel(ctx), rec)
	}
	return &maitrev1.PutRecordResponse{Record: rec}, nil
}

// removedNode reports whether id names a Node removed from the Fleet.
func (s *Service) removedNode(ctx context.Context, id string) bool {
	rec, err := s.store.Raw(ctx, store.KindNode, id)
	return err == nil && rec.GetDeleted()
}

// PushRecords implements NodeService: a peer's records, applied newest-wins.
func (s *Service) PushRecords(ctx context.Context, req *maitrev1.PushRecordsRequest) (*maitrev1.PushRecordsResponse, error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node has no Fleet database (dev mode)")
	}
	// One Record that cannot be applied must not cost the rest of the
	// batch: apply everything else, then report the first failure.
	var applied, failed uint32
	var firstErr error
	for _, r := range req.GetRecords() {
		ok, err := s.store.Apply(ctx, r)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s %s: %w", r.GetKind(), r.GetId(), err)
			}
			continue
		}
		if ok {
			applied++
			if s.OnChange != nil {
				s.OnChange(r)
			}
		}
	}
	if failed > 1 {
		return nil, connect.Errorf(connect.CodeInternal, "%v (and %d more records)", firstErr, failed-1)
	}
	if firstErr != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", firstErr)
	}
	return &maitrev1.PushRecordsResponse{Applied: applied}, nil
}

// GetNodeMetrics implements NodeService.
func (s *Service) GetNodeMetrics(context.Context, *maitrev1.GetNodeMetricsRequest) (*maitrev1.GetNodeMetricsResponse, error) {
	if s.Metrics == nil {
		return &maitrev1.GetNodeMetricsResponse{Metrics: &maitrev1.NodeMetrics{}}, nil
	}
	return &maitrev1.GetNodeMetricsResponse{Metrics: s.Metrics()}, nil
}

// OwnStatus is what this Node gossips (ADR 0006): its metrics and Services now.
func (s *Service) OwnStatus(ctx context.Context) *maitrev1.FleetNodeStatus {
	now := time.Now().UnixMilli()
	self := &maitrev1.FleetNodeStatus{NodeId: s.id.Fingerprint(), Reachable: true, LastSeenUnixMs: now, MetricsUnixMs: now, ServicesUnixMs: now, Version: s.info.Version, TimeZone: timezone.Local()}
	if s.Metrics != nil {
		self.Metrics = s.Metrics()
	} else {
		self.Metrics = &maitrev1.NodeMetrics{}
	}
	if s.Services != nil {
		self.Services = s.Services(ctx)
	}
	if st := s.cachedUpdate(ctx); st != nil {
		self.UpdateState, self.UpdateMessage, self.UpdateProgress = st.GetState(), st.GetMessage(), st.GetProgress()
		self.UpdateStaged, self.LastRollback = st.GetStaged() != nil && !st.GetRollbackQueued(), st.GetLastRollback()
		if self.UpdateStaged {
			self.StagedVersion = st.GetStaged().GetVersion()
		}
		self.UpdateAvailable = st.GetAvailable() != ""
	}
	if s.Peers != nil {
		self.SyncGaps = s.Peers.Gaps()
	}
	if s.Entries != nil {
		self.Entries = s.Entries()
	}
	if s.Parity != nil {
		self.Parity = s.Parity(ctx)
	}
	if s.Drains != nil {
		for _, j := range s.Drains() {
			if j.GetState() == "queued" || j.GetState() == "running" {
				self.Drains = append(self.Drains, j)
			}
		}
	}
	if s.Copies != nil {
		self.Copies = s.Copies()
	}
	if s.Mirrors != nil {
		self.Mirrors = s.Mirrors(ctx)
	}
	return self
}

func (s *Service) cachedUpdate(ctx context.Context) *maitrev1.UpdateStatus {
	if s.Updates == nil {
		return nil
	}
	s.updMu.Lock()
	defer s.updMu.Unlock()
	every := 30 * time.Second
	if s.updLast != nil && (s.updLast.GetState() == maitrev1.UpdateStatus_STATE_UPDATING || s.updLast.GetState() == maitrev1.UpdateStatus_STATE_REBOOTING) {
		every = 5 * time.Second
	}
	if s.updLast == nil || time.Since(s.updAt) >= every {
		if st, err := s.Updates.Status(ctx); err == nil {
			s.decorate(st)
			s.updLast = st
		}
		s.updAt = time.Now()
	}
	return s.updLast
}

// GetFleetStatus implements NodeService (ADR 0006): this Node's own status,
// then every peer with its reachability as seen from here (ADR 0007), its
// version and the problems calling it, the last metrics, Services and
// update state it gossiped, and, for a Node still being installed, what
// its Installer reports.
func (s *Service) GetFleetStatus(ctx context.Context, _ *maitrev1.GetFleetStatusRequest) (*maitrev1.GetFleetStatusResponse, error) {
	self := s.OwnStatus(ctx)
	out := &maitrev1.GetFleetStatusResponse{NodeId: self.NodeId, Nodes: []*maitrev1.FleetNodeStatus{self}}
	if s.Peers != nil {
		for _, p := range s.Peers.Status(ctx) {
			st := &maitrev1.FleetNodeStatus{NodeId: p.GetNodeId(), Reachable: p.GetOnline(), LastSeenUnixMs: p.GetLastSeenUnixMs(), Version: p.GetVersion(), Problems: p.GetProblems()}
			if last := s.Peers.Last(p.GetNodeId()); last != nil {
				st.Metrics, st.MetricsUnixMs = last.GetMetrics(), last.GetMetricsUnixMs()
				st.Services, st.ServicesUnixMs = last.GetServices(), last.GetServicesUnixMs()
				st.UpdateState, st.UpdateMessage, st.UpdateProgress = last.GetUpdateState(), last.GetUpdateMessage(), last.GetUpdateProgress()
				st.UpdateStaged, st.LastRollback = last.GetUpdateStaged(), last.GetLastRollback()
				st.StagedVersion = last.GetStagedVersion()
				st.UpdateAvailable = last.GetUpdateAvailable()
				st.SyncGaps = last.GetSyncGaps()
				st.Entries = last.GetEntries()
				st.Parity, st.Drains = last.GetParity(), last.GetDrains()
				st.Copies, st.Mirrors = last.GetCopies(), last.GetMirrors()
				st.TimeZone = last.GetTimeZone()
			}
			st.Provisioning = s.Peers.Installing(p.GetNodeId())
			out.Nodes = append(out.Nodes, st)
		}
	}
	return out, nil
}

// PushStatus implements NodeService: a peer's gossip, kept in memory.
func (s *Service) PushStatus(_ context.Context, req *maitrev1.PushStatusRequest) (*maitrev1.PushStatusResponse, error) {
	if s.Peers != nil {
		s.Peers.Received(req.GetStatus())
	}
	return &maitrev1.PushStatusResponse{}, nil
}

// GetUpdateStatus implements NodeService.
func (s *Service) GetUpdateStatus(ctx context.Context, _ *maitrev1.GetUpdateStatusRequest) (*maitrev1.GetUpdateStatusResponse, error) {
	if s.Updates == nil {
		return &maitrev1.GetUpdateStatusResponse{Status: &maitrev1.UpdateStatus{State: maitrev1.UpdateStatus_STATE_IDLE}}, nil
	}
	st, err := s.Updates.Status(ctx)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	s.decorate(st)
	if s.PokeDepots != nil {
		s.PokeDepots()
	}
	return &maitrev1.GetUpdateStatusResponse{Status: st}, nil
}

// decorate adds to bootc's status what the Node knows around it: the
// rollout's last word, the Depots' listings, and a newer digest for the
// booted image, from the registry check or, sooner, from a Depot's listing.
func (s *Service) decorate(st *maitrev1.UpdateStatus) {
	if s.Rollout != nil {
		st.Rollout = s.Rollout()
	}
	if s.Runs != nil {
		st.Runs = s.Runs()
	}
	if s.Offers != nil {
		st.DepotImages = s.Offers()
	}
	newest := ""
	if s.Available != nil {
		newest = s.Available()
	}
	if newest == "" {
		newest = depots.Offered(st.GetBooted().GetImage(), st.GetDepotImages())
	}
	st.Available = rollout.Newer(st, newest)
}

// UpdateImage implements NodeService.
func (s *Service) UpdateImage(ctx context.Context, req *maitrev1.UpdateImageRequest) (*maitrev1.UpdateImageResponse, error) {
	if s.Updates == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node does not run on bootc (dev mode)")
	}
	// Who asked, recorded on the run: a version that changed is otherwise
	// traceable to nobody, and a staged update that applies at some later
	// reboot is exactly the case where that matters.
	by := ""
	if p, ok := peer.From(ctx); ok {
		by = p.Fingerprint
	}
	switch err := s.Updates.Start(req.GetImage(), req.GetReboot(), req.GetInsecureRegistry(), by); {
	case errors.Is(err, update.ErrBusy):
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	case err != nil:
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
	}
	return &maitrev1.UpdateImageResponse{}, nil
}

// Rollback implements NodeService.
func (s *Service) Rollback(ctx context.Context, req *maitrev1.RollbackRequest) (*maitrev1.RollbackResponse, error) {
	if s.Updates == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node does not run on bootc (dev mode)")
	}
	switch err := s.Updates.Rollback(ctx, req.GetReboot()); {
	case errors.Is(err, update.ErrBusy):
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	case err != nil:
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	return &maitrev1.RollbackResponse{}, nil
}

// Reboot implements NodeService.
// RenameNode implements NodeService.
func (s *Service) RenameNode(ctx context.Context, req *maitrev1.RenameNodeRequest) (*maitrev1.RenameNodeResponse, error) {
	name := strings.TrimSpace(req.GetName())
	if name == "" || len(name) > 40 {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "a Node name is 1 to 40 characters")
	}
	if s.Rename == nil {
		return nil, connect.Errorf(connect.CodeUnimplemented, "this Node cannot be renamed (dev mode)")
	}
	n, err := s.Rename(ctx, name)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	s.info.Name = name
	return &maitrev1.RenameNodeResponse{Node: n}, nil
}

func (s *Service) Reboot(ctx context.Context, _ *maitrev1.RebootRequest) (*maitrev1.RebootResponse, error) {
	if s.Updates == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node does not run on bootc (dev mode)")
	}
	if err := s.Updates.Reboot(ctx); err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	return &maitrev1.RebootResponse{}, nil
}

func errNoHistory() error {
	return connect.NewError(connect.CodeUnimplemented, "this Node keeps no metrics history")
}

// GetMetricsHistory implements NodeService (decision 210).
func (s *Service) GetMetricsHistory(_ context.Context, in *maitrev1.GetMetricsHistoryRequest) (*maitrev1.GetMetricsHistoryResponse, error) {
	if s.History == nil {
		return nil, errNoHistory()
	}
	series, err := s.History.Query(in.GetSeries(), in.GetFromUnixMs(), in.GetToUnixMs(), min(int(in.GetPoints()), 5000))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return &maitrev1.GetMetricsHistoryResponse{Series: series}, nil
}

// ExportMetrics implements NodeService: a Service's series, for the Node
// it moves to.
func (s *Service) ExportMetrics(_ context.Context, in *maitrev1.ExportMetricsRequest) (*maitrev1.ExportMetricsResponse, error) {
	if s.History == nil {
		return nil, errNoHistory()
	}
	files, err := s.History.Export(in.GetService())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return &maitrev1.ExportMetricsResponse{Files: files}, nil
}

// ImportMetrics implements NodeService: a Service that moved here keeps
// its graphs.
func (s *Service) ImportMetrics(_ context.Context, in *maitrev1.ImportMetricsRequest) (*maitrev1.ImportMetricsResponse, error) {
	if s.History == nil {
		return nil, errNoHistory()
	}
	if err := s.History.Import(in.GetService(), in.GetFiles()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return &maitrev1.ImportMetricsResponse{}, nil
}

// debugCaller is who may use the debug tier (decisions 323, 326): a
// Trusted Device, standing in for the Debug Console until that app
// exists, and never a Node — nothing in the Fleet protocol needs these,
// so a Node asking is a Node that has been taken over.
func (s *Service) debugCaller(ctx context.Context) error {
	p, ok := peer.From(ctx)
	if !ok {
		return connect.NewError(connect.CodePermissionDenied, "the debug console needs a Trusted Device")
	}
	if s.IsNode != nil && s.IsNode(p.Fingerprint) {
		return connect.NewError(connect.CodePermissionDenied, "the debug console is for a Device, not for another Node")
	}
	if s.Limited != nil && s.Limited(p.Fingerprint) {
		return connect.NewError(connect.CodePermissionDenied, "the Recovery Phrase may only add a Trusted Device")
	}
	return nil
}

// DebugReadFile reads a file for diagnosis, if this image was built for it
// and the allowlist covers the path (decision 326).
func (s *Service) DebugReadFile(ctx context.Context, in *maitrev1.DebugReadFileRequest) (*maitrev1.DebugReadFileResponse, error) {
	if err := s.debugCaller(ctx); err != nil {
		return nil, err
	}
	b, err := debugsurface.ReadFile(in.GetPath())
	if err != nil {
		if errors.Is(err, debugsurface.ErrNotADebugBuild) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return &maitrev1.DebugReadFileResponse{Content: b}, nil
}

// DebugExec runs a command on the Node, in an image built for it only.
func (s *Service) DebugExec(ctx context.Context, in *maitrev1.DebugExecRequest) (*maitrev1.DebugExecResponse, error) {
	if err := s.debugCaller(ctx); err != nil {
		return nil, err
	}
	out, err := debugsurface.Exec(ctx, in.GetCommand(), in.GetArgs())
	if err != nil {
		if errors.Is(err, debugsurface.ErrNotADebugBuild) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
		}
		// The output is the diagnosis even when the command failed.
		return &maitrev1.DebugExecResponse{Output: out}, connect.NewError(connect.CodeAborted, err.Error())
	}
	return &maitrev1.DebugExecResponse{Output: out}, nil
}
