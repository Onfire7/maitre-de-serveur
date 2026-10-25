// Package storageservice implements maitre.v1.StorageService over storage.Manager.
package storageservice

import (
	"context"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
)

// Service answers StorageService calls.
type Service struct {
	m *storage.Manager
}

var _ maitrev1connect.StorageServiceHandler = (*Service)(nil)

// New wraps a Manager.
func New(m *storage.Manager) *Service { return &Service{m: m} }

func (s *Service) ListDrives(ctx context.Context, _ *maitrev1.ListDrivesRequest) (*maitrev1.ListDrivesResponse, error) {
	drives, err := s.m.List(ctx)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	return &maitrev1.ListDrivesResponse{Drives: drives}, nil
}

func (s *Service) DriveContents(ctx context.Context, req *maitrev1.DriveContentsRequest) (*maitrev1.DriveContentsResponse, error) {
	res, err := s.m.Contents(ctx, req.GetDevice())
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return res, nil
}

func (s *Service) AdoptDrive(ctx context.Context, req *maitrev1.AdoptDriveRequest) (*maitrev1.AdoptDriveResponse, error) {
	d, err := s.m.Adopt(ctx, req.GetDevice(), req.GetName(), req.GetTags())
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.AdoptDriveResponse{Drive: d}, nil
}

func (s *Service) FormatDrive(ctx context.Context, req *maitrev1.FormatDriveRequest) (*maitrev1.FormatDriveResponse, error) {
	d, err := s.m.Format(ctx, req.GetDevice(), req.GetName(), req.GetTags(), req.GetConfirm())
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.FormatDriveResponse{Drive: d}, nil
}

func (s *Service) SetDriveTags(ctx context.Context, req *maitrev1.SetDriveTagsRequest) (*maitrev1.SetDriveTagsResponse, error) {
	d, err := s.m.SetTags(ctx, req.GetDriveId(), req.GetTags(), req.GetName())
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.SetDriveTagsResponse{Drive: d}, nil
}

func (s *Service) ForgetDrive(ctx context.Context, req *maitrev1.ForgetDriveRequest) (*maitrev1.ForgetDriveResponse, error) {
	if err := s.m.Forget(ctx, req.GetDriveId(), req.GetConfirm()); err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.ForgetDriveResponse{}, nil
}
