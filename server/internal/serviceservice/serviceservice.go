// Package serviceservice implements maitre.v1.ServiceService on top of the
// services Manager.
package serviceservice

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
)

// Catalog holds the current index and where it came from.
type Catalog struct {
	mu      sync.RWMutex
	index   *catalog.Index
	raw     []byte
	source  string
	fetched time.Time
}

// NewCatalog wraps an index loaded from source.
func NewCatalog(idx *catalog.Index, source string) *Catalog {
	c := &Catalog{}
	c.Set(idx, source)
	return c
}

// Set replaces the index.
func (c *Catalog) Set(idx *catalog.Index, source string) {
	raw, _ := json.Marshal(idx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.index, c.raw, c.source, c.fetched = idx, raw, source, time.Now()
}

// Index returns the current index (nil if none).
func (c *Catalog) Index() *catalog.Index {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.index
}

// Service answers ServiceService calls.
type Service struct {
	mgr *services.Manager
	cat *Catalog
	// OnChange runs after installs and removals so Caddy and mDNS can follow.
	OnChange func()
}

var _ maitrev1connect.ServiceServiceHandler = (*Service)(nil)

// New builds the handler.
func New(mgr *services.Manager, cat *Catalog) *Service {
	return &Service{mgr: mgr, cat: cat}
}

func (s *Service) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

// GetCatalog implements ServiceService.
func (s *Service) GetCatalog(context.Context, *maitrev1.GetCatalogRequest) (*maitrev1.GetCatalogResponse, error) {
	s.cat.mu.RLock()
	defer s.cat.mu.RUnlock()
	if s.cat.index == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "this Node has no Catalog yet")
	}
	return &maitrev1.GetCatalogResponse{IndexJson: s.cat.raw, Source: s.cat.source, FetchedUnixMs: s.cat.fetched.UnixMilli()}, nil
}

// InstallService implements ServiceService.
func (s *Service) InstallService(ctx context.Context, req *maitrev1.InstallServiceRequest) (*maitrev1.InstallServiceResponse, error) {
	st, err := s.mgr.Install(ctx, req.GetSpec())
	if err != nil {
		return nil, mapErr(err)
	}
	s.changed()
	return &maitrev1.InstallServiceResponse{Service: st}, nil
}

// ControlService implements ServiceService.
func (s *Service) ControlService(ctx context.Context, req *maitrev1.ControlServiceRequest) (*maitrev1.ControlServiceResponse, error) {
	st, err := s.mgr.Control(ctx, req)
	if err != nil {
		return nil, mapErr(err)
	}
	if req.GetAction() == maitrev1.ControlServiceRequest_ACTION_REMOVE {
		s.changed()
	}
	return &maitrev1.ControlServiceResponse{Service: st}, nil
}

// ListServices implements ServiceService.
func (s *Service) ListServices(ctx context.Context, _ *maitrev1.ListServicesRequest) (*maitrev1.ListServicesResponse, error) {
	list, err := s.mgr.List(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return &maitrev1.ListServicesResponse{Services: list}, nil
}

// SetExposure implements ServiceService.
func (s *Service) SetExposure(ctx context.Context, req *maitrev1.SetExposureRequest) (*maitrev1.SetExposureResponse, error) {
	st, err := s.mgr.SetExposure(ctx, req.GetName(), req.GetExposure(), req.GetPublicHost())
	if err != nil {
		return nil, mapErr(err)
	}
	s.changed()
	return &maitrev1.SetExposureResponse{Service: st}, nil
}

// SetFolders implements ServiceService (decisions 170, 171, 174).
func (s *Service) SetFolders(ctx context.Context, req *maitrev1.SetFoldersRequest) (*maitrev1.SetFoldersResponse, error) {
	st, err := s.mgr.SetFolders(ctx, req)
	if err != nil {
		return nil, mapErr(err)
	}
	s.changed()
	return &maitrev1.SetFoldersResponse{Service: st}, nil
}

// SetSettings implements ServiceService (decision 193).
func (s *Service) SetSettings(ctx context.Context, req *maitrev1.SetSettingsRequest) (*maitrev1.SetSettingsResponse, error) {
	st, err := s.mgr.SetSettings(ctx, req)
	if err != nil {
		return nil, mapErr(err)
	}
	s.changed()
	return &maitrev1.SetSettingsResponse{Service: st}, nil
}

// SetImage implements ServiceService (decision 195).
func (s *Service) SetImage(ctx context.Context, req *maitrev1.SetImageRequest) (*maitrev1.SetImageResponse, error) {
	st, err := s.mgr.SetImage(ctx, req.GetName(), req.GetImage(), req.GetComponent())
	if err != nil {
		return nil, mapErr(err)
	}
	s.changed()
	return &maitrev1.SetImageResponse{Service: st}, nil
}

// SetPortForwarding implements ServiceService (decision 115).
func (s *Service) SetPortForwarding(ctx context.Context, req *maitrev1.SetPortForwardingRequest) (*maitrev1.SetPortForwardingResponse, error) {
	st, err := s.mgr.SetPortForwarding(ctx, req.GetName(), req.GetEnabled())
	if err != nil {
		return nil, mapErr(err)
	}
	s.changed()
	return &maitrev1.SetPortForwardingResponse{Service: st}, nil
}

// MoveVolume implements ServiceService.
func (s *Service) MoveVolume(ctx context.Context, req *maitrev1.MoveVolumeRequest) (*maitrev1.MoveVolumeResponse, error) {
	id, err := s.mgr.MoveVolume(ctx, req.GetName(), req.GetVolume(), req.GetToSpace())
	if err != nil {
		return nil, mapErr(err)
	}
	return &maitrev1.MoveVolumeResponse{JobId: id}, nil
}

// GetServiceLogs implements ServiceService.
func (s *Service) GetServiceLogs(ctx context.Context, req *maitrev1.GetServiceLogsRequest) (*maitrev1.GetServiceLogsResponse, error) {
	lines, err := s.mgr.Logs(ctx, req.GetName(), req.GetComponent(), req.GetLines())
	if err != nil {
		return nil, mapErr(err)
	}
	return &maitrev1.GetServiceLogsResponse{Lines: lines}, nil
}

// CheckService implements ServiceService (plan C10): the Node's own check
// of a spec, without installing it.
func (s *Service) CheckService(ctx context.Context, req *maitrev1.CheckServiceRequest) (*maitrev1.CheckServiceResponse, error) {
	if req.GetSpec() == nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "no service spec")
	}
	res, err := services.Check(ctx, req.GetSpec())
	if err != nil {
		if errors.Is(err, quadlet.ErrNoGenerator) {
			return nil, connect.Errorf(connect.CodeUnavailable, "%v", err)
		}
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
	}
	out := &maitrev1.CheckServiceResponse{Units: res.Units}
	for _, p := range res.Protected {
		out.Protected = append(out.Protected, &maitrev1.ProtectedOption{Component: p.Component, ImageRepository: p.ImageRepository, Option: p.Option})
	}
	return out, nil
}

func mapErr(err error) error {
	// Decision 189: the Console asks a Trusted Device for these and retries.
	var ge *services.GrantsError
	if errors.As(err, &ge) {
		ce := connect.NewError(connect.CodeFailedPrecondition, err.Error())
		if d, derr := connectproto.NewErrorDetail(ge.Needed); derr == nil {
			ce = ce.WithDetail(d)
		}
		return ce
	}
	switch {
	case errors.Is(err, services.ErrExists):
		return connect.Errorf(connect.CodeAlreadyExists, "%v", err)
	case errors.Is(err, services.ErrNotFound):
		return connect.Errorf(connect.CodeNotFound, "%v", err)
	}
	return connect.Errorf(connect.CodeInternal, "%v", err)
}
