// Package filesservice adapts files.Manager to the FilesService RPC.
package filesservice

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/files"
)

// Service answers FilesService.
type Service struct{ m *files.Manager }

// New wraps a Manager.
func New(m *files.Manager) *Service { return &Service{m: m} }

func (s *Service) ListDir(ctx context.Context, req *maitrev1.ListDirRequest) (*maitrev1.ListDirResponse, error) {
	entries, err := s.m.ListDir(ctx, req.GetScope(), req.GetPath())
	if err != nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
	}
	return &maitrev1.ListDirResponse{Entries: entries}, nil
}

func (s *Service) Usage(ctx context.Context, req *maitrev1.UsageRequest) (*maitrev1.UsageResponse, error) {
	res, err := s.m.Usage(ctx, req.GetScope(), req.GetPath())
	if err != nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
	}
	return res, nil
}

func (s *Service) Move(ctx context.Context, req *maitrev1.MoveRequest) (*maitrev1.MoveResponse, error) {
	j, err := s.m.Move(ctx, req)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
	}
	return &maitrev1.MoveResponse{JobId: j.GetJobId()}, nil
}

func (s *Service) ListJobs(context.Context, *maitrev1.ListJobsRequest) (*maitrev1.ListJobsResponse, error) {
	return &maitrev1.ListJobsResponse{Jobs: s.m.Jobs()}, nil
}

func (s *Service) CancelJob(_ context.Context, req *maitrev1.CancelJobRequest) (*maitrev1.CancelJobResponse, error) {
	if err := s.m.Cancel(req.GetJobId()); err != nil {
		return nil, connect.Errorf(connect.CodeNotFound, "%v", err)
	}
	return &maitrev1.CancelJobResponse{}, nil
}

func (s *Service) MakeDir(ctx context.Context, req *maitrev1.MakeDirRequest) (*maitrev1.MakeDirResponse, error) {
	if err := s.m.MakeDir(ctx, req.GetScope(), req.GetPath()); err != nil {
		if errors.Is(err, files.ErrExists) {
			return nil, connect.Errorf(connect.CodeAlreadyExists, "%v", err)
		}
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
	}
	return &maitrev1.MakeDirResponse{}, nil
}

func (s *Service) Zip(ctx context.Context, req *maitrev1.ZipRequest) (*maitrev1.ZipResponse, error) {
	j, name, err := s.m.Zip(ctx, req)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
	}
	return &maitrev1.ZipResponse{JobId: j.GetJobId(), Name: name}, nil
}

func (s *Service) Unzip(ctx context.Context, req *maitrev1.UnzipRequest) (*maitrev1.UnzipResponse, error) {
	j, folder, err := s.m.Unzip(ctx, req)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
	}
	return &maitrev1.UnzipResponse{JobId: j.GetJobId(), Folder: folder}, nil
}
