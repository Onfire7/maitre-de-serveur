// Package nodeservice implements maitre.v1.NodeService.
package nodeservice

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
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
	// Updates drives bootc; nil means this Node cannot update itself (dev mode).
	Updates *update.Manager
	// Peers forwards writes to their owner, pushes ours, and reports the
	// Fleet's other Nodes. Nil on a single-Node or dev setup.
	Peers Peers
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
	case store.KindFleet, store.KindTrustedDevice, "tier", "tailscale", "update_policy", "public_settings":
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
func (s *Service) Reboot(ctx context.Context, _ *maitrev1.RebootRequest) (*maitrev1.RebootResponse, error) {
	if s.Updates == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node does not run on bootc (dev mode)")
	}
	if err := s.Updates.Reboot(ctx); err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	return &maitrev1.RebootResponse{}, nil
}
