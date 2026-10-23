// Package nodeservice implements maitre.v1.NodeService.
package nodeservice

import (
	"context"
	"time"

	maitrev1 "github.com/maitre-de-serveur/maitre/server/gen/maitre/v1"
	"github.com/maitre-de-serveur/maitre/server/gen/maitre/v1/maitrev1connect"
	"github.com/maitre-de-serveur/maitre/server/internal/identity"
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
	started time.Time
}

var _ maitrev1connect.NodeServiceHandler = (*Service)(nil)

// New builds a Service for the given identity.
func New(id *identity.Identity, info Info) *Service {
	return &Service{id: id, info: info, started: time.Now()}
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
