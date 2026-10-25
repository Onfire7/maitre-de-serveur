// Package nodeservice implements maitre.v1.NodeService.
package nodeservice

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
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
}

var _ maitrev1connect.NodeServiceHandler = (*Service)(nil)

// New builds a Service for the given identity. st may be nil.
func New(id *identity.Identity, info Info, st *store.Store) *Service {
	return &Service{id: id, info: info, store: st, started: time.Now()}
}

// GetNodeInfo implements NodeService.
func (s *Service) GetNodeInfo(_ context.Context, _ *maitrev1.GetNodeInfoRequest) (*maitrev1.GetNodeInfoResponse, error) {
	return &maitrev1.GetNodeInfoResponse{
		Node: &maitrev1.NodeInfo{
			NodeId:        s.id.Fingerprint(),
			Name:          s.info.Name,
			FleetId:       s.info.FleetID,
			Version:       s.info.Version,
			UptimeSeconds: uint64(time.Since(s.started).Seconds()),
			DevMode:       s.info.DevMode,
		},
	}, nil
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
	case store.KindFleet, store.KindTrustedDevice:
		return true
	}
	return false
}

// PutRecord implements NodeService. Milestone 1 is single-Node: a write to a
// Record owned by another Node is refused; forwarding and queueing arrive
// with the sync layer in Milestone 3.
func (s *Service) PutRecord(ctx context.Context, req *maitrev1.PutRecordRequest) (*maitrev1.PutRecordResponse, error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node has no Fleet database (dev mode)")
	}
	in := req.GetRecord()
	if in.GetKind() == "" || in.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "record needs kind and id")
	}
	var (
		rec *maitrev1.Record
		err error
	)
	fleetWide := FleetWide(in.GetKind())
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
	return &maitrev1.PutRecordResponse{Record: rec}, nil
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
