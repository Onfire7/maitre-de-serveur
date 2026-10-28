// Package filesservice adapts files.Manager to the FilesService RPC.
package filesservice

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/files"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
)

// Service answers FilesService.
type Service struct {
	m *files.Manager
	// IsNode reports whether a caller is one of the Fleet's Nodes, whose
	// moves are limited (decision 189); nil treats no caller as a Node.
	IsNode func(fingerprint string) bool
}

// New wraps a Manager.
func New(m *files.Manager) *Service { return &Service{m: m} }

// node returns the calling Node's id, or "" when the caller is not a Node.
func (s *Service) node(ctx context.Context) string {
	if p, ok := peer.From(ctx); ok && s.IsNode != nil && s.IsNode(p.Fingerprint) {
		return p.Fingerprint
	}
	return ""
}

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
	if n := s.node(ctx); n != "" {
		// A Node hands us a move a person started on it (files.forward).
		if err := s.m.CheckPeerMove(ctx, n, req); err != nil {
			return nil, connect.Errorf(connect.CodePermissionDenied, "%v", err)
		}
	}
	j, err := s.m.Move(ctx, req)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%v", err)
	}
	return &maitrev1.MoveResponse{JobId: j.GetJobId()}, nil
}

func (s *Service) ListJobs(context.Context, *maitrev1.ListJobsRequest) (*maitrev1.ListJobsResponse, error) {
	return &maitrev1.ListJobsResponse{Jobs: s.m.Jobs()}, nil
}

func (s *Service) CancelJob(ctx context.Context, req *maitrev1.CancelJobRequest) (*maitrev1.CancelJobResponse, error) {
	if n := s.node(ctx); n != "" {
		// A Node may cancel only the moves it handed us.
		mine := false
		for _, j := range s.m.Jobs() {
			mine = mine || (j.GetJobId() == req.GetJobId() && j.GetRequest().GetFromNodeId() == n)
		}
		if !mine {
			return nil, connect.Errorf(connect.CodePermissionDenied, "a Node may cancel only its own moves")
		}
	}
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

func (s *Service) Delete(ctx context.Context, req *maitrev1.DeleteRequest) (*maitrev1.DeleteResponse, error) {
	j, err := s.m.Delete(ctx, req)
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.DeleteResponse{JobId: j.GetJobId()}, nil
}
