// Package nodeservice implements maitre.v1.NodeService.
package nodeservice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
)

// Info is the static part of what GetNodeInfo reports.
type Info struct {
	Name    string
	FleetID string
	Version string
	DevMode bool
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
	// Metrics supplies GetNodeMetrics; nil means empty readings.
	Metrics func() *maitrev1.NodeMetrics
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
	// Limited reports whether a caller key is recovery-only (decision 100):
	// such a caller may only add a Trusted Device.
	Limited func(fingerprint string) bool
	// Tailscale reports the Node's tsnet state; nil when not joined.
	Tailscale func() *maitrev1.TailscaleStatus
	// Rollout reports the staged rollout's last decision; nil if none.
	Rollout func() string
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
	case store.KindFleet, store.KindTrustedDevice, "space", "tailscale", "update_policy", "public_settings":
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
	if in.GetKind() == store.KindNode && in.GetId() != s.id.Fingerprint() && in.GetDeleted() {
		// Removing a Node from the Fleet (guard on the Console side): a
		// tombstone in the Node's own name, so it wins everywhere.
		rec, err := s.store.Tombstone(ctx, store.KindNode, in.GetId())
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
		added, err := s.store.Introduce(ctx, &n)
		if err != nil {
			return nil, connect.Errorf(connect.CodeInternal, "%v", err)
		}
		rec, err := s.store.Get(ctx, store.KindNode, in.GetId(), &maitrev1.Node{})
		if err != nil {
			return nil, connect.Errorf(connect.CodeInternal, "%v", err)
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
	if in.GetDeleted() {
		rec, err = s.store.Delete(ctx, in.GetKind(), in.GetId(), fleetWide)
	} else {
		rec, err = s.store.PutRaw(ctx, in.GetKind(), in.GetId(), in.GetBody(), fleetWide)
	}
	switch {
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

// PushRecords implements NodeService: a peer's records, applied newest-wins.
func (s *Service) PushRecords(ctx context.Context, req *maitrev1.PushRecordsRequest) (*maitrev1.PushRecordsResponse, error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node has no Fleet database (dev mode)")
	}
	var applied uint32
	for _, r := range req.GetRecords() {
		ok, err := s.store.Apply(ctx, r)
		if err != nil {
			return nil, connect.Errorf(connect.CodeInternal, "%v", err)
		}
		if ok {
			applied++
			if s.OnChange != nil {
				s.OnChange(r)
			}
		}
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
	self := &maitrev1.FleetNodeStatus{NodeId: s.id.Fingerprint(), Reachable: true, LastSeenUnixMs: now, MetricsUnixMs: now, ServicesUnixMs: now}
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
			if s.Rollout != nil {
				st.Rollout = s.Rollout()
			}
			s.updLast = st
		}
		s.updAt = time.Now()
	}
	return s.updLast
}

// GetFleetStatus implements NodeService (ADR 0006): this Node's own status,
// then every peer with its reachability as seen from here and the last
// metrics and Services it gossiped.
func (s *Service) GetFleetStatus(ctx context.Context, _ *maitrev1.GetFleetStatusRequest) (*maitrev1.GetFleetStatusResponse, error) {
	self := s.OwnStatus(ctx)
	out := &maitrev1.GetFleetStatusResponse{NodeId: self.NodeId, Nodes: []*maitrev1.FleetNodeStatus{self}}
	if s.Peers != nil {
		for _, p := range s.Peers.Status(ctx) {
			st := &maitrev1.FleetNodeStatus{NodeId: p.GetNodeId(), Reachable: p.GetOnline(), LastSeenUnixMs: p.GetLastSeenUnixMs()}
			if last := s.Peers.Last(p.GetNodeId()); last != nil {
				st.Metrics, st.MetricsUnixMs = last.GetMetrics(), last.GetMetricsUnixMs()
				st.Services, st.ServicesUnixMs = last.GetServices(), last.GetServicesUnixMs()
			}
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
	if s.Rollout != nil {
		st.Rollout = s.Rollout()
	}
	return &maitrev1.GetUpdateStatusResponse{Status: st}, nil
}

// UpdateImage implements NodeService.
func (s *Service) UpdateImage(_ context.Context, req *maitrev1.UpdateImageRequest) (*maitrev1.UpdateImageResponse, error) {
	if s.Updates == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node does not run on bootc (dev mode)")
	}
	switch err := s.Updates.Start(req.GetImage(), req.GetReboot(), req.GetInsecureRegistry()); {
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
