// Package storageservice implements maitre.v1.StorageService over storage.Manager.
package storageservice

import (
	"context"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/copies"
	"github.com/Onfire7/maitre-de-serveur/server/internal/drain"
	"github.com/Onfire7/maitre-de-serveur/server/internal/parity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
)

// Service answers StorageService calls.
type Service struct {
	m *storage.Manager
	// Drains runs Drain jobs (ADR 0009); nil answers Unimplemented, as an
	// older Node does.
	Drains *drain.Manager
	// Parity runs SnapRAID (ADR 0009); nil answers Unimplemented.
	Parity *parity.Manager
	// Copies keeps Spaces' copies; nil answers Unimplemented.
	Copies *copies.Manager
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

var errNoDrain = connect.NewError(connect.CodeUnimplemented, "this Node cannot empty Drives")

func (s *Service) PlanDrain(ctx context.Context, req *maitrev1.PlanDrainRequest) (*maitrev1.PlanDrainResponse, error) {
	if s.Drains == nil {
		return nil, errNoDrain
	}
	plan, err := s.Drains.Plan(ctx, req.GetDriveId(), req.GetLooseToSpace(), nil)
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.PlanDrainResponse{Plan: plan}, nil
}

func (s *Service) DrainDrive(ctx context.Context, req *maitrev1.DrainDriveRequest) (*maitrev1.DrainDriveResponse, error) {
	if s.Drains == nil {
		return nil, errNoDrain
	}
	job, err := s.Drains.Start(ctx, req.GetDriveId(), req.GetLooseToSpace(), req.GetConfirm(), req.GetPurpose())
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.DrainDriveResponse{Job: job}, nil
}

func (s *Service) ListDrains(context.Context, *maitrev1.ListDrainsRequest) (*maitrev1.ListDrainsResponse, error) {
	if s.Drains == nil {
		return nil, errNoDrain
	}
	return &maitrev1.ListDrainsResponse{Jobs: s.Drains.Jobs()}, nil
}

func (s *Service) CancelDrain(_ context.Context, req *maitrev1.CancelDrainRequest) (*maitrev1.CancelDrainResponse, error) {
	if s.Drains == nil {
		return nil, errNoDrain
	}
	if err := s.Drains.Cancel(req.GetJobId()); err != nil {
		return nil, connect.Errorf(connect.CodeNotFound, "%v", err)
	}
	return &maitrev1.CancelDrainResponse{}, nil
}

var errNoParity = connect.NewError(connect.CodeUnimplemented, "this Node has no parity support")

func (s *Service) CheckParityDrive(ctx context.Context, req *maitrev1.CheckParityDriveRequest) (*maitrev1.CheckParityDriveResponse, error) {
	if s.Parity == nil {
		return nil, errNoParity
	}
	res, err := s.Parity.Check(ctx, req.GetDriveId(), req.GetDevice(), req.GetLooseToSpace())
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return res, nil
}

func (s *Service) SetParityDrive(ctx context.Context, req *maitrev1.SetParityDriveRequest) (*maitrev1.SetParityDriveResponse, error) {
	if s.Parity == nil {
		return nil, errNoParity
	}
	res, err := s.Parity.SetParity(ctx, req)
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return res, nil
}

func (s *Service) ProtectNow(ctx context.Context, _ *maitrev1.ProtectNowRequest) (*maitrev1.ProtectNowResponse, error) {
	if s.Parity == nil {
		return nil, errNoParity
	}
	go func() {
		if err := s.Parity.Sync(context.WithoutCancel(ctx), true); err != nil {
			_ = err // reported in the status
		}
	}()
	return &maitrev1.ProtectNowResponse{}, nil
}

func (s *Service) ReplaceDrive(ctx context.Context, req *maitrev1.ReplaceDriveRequest) (*maitrev1.ReplaceDriveResponse, error) {
	if s.Parity == nil {
		return nil, errNoParity
	}
	d, err := s.Parity.Replace(ctx, req.GetFailedDriveId(), req.GetDevice(), req.GetConfirm())
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.ReplaceDriveResponse{Drive: d}, nil
}

func (s *Service) GetParityStatus(ctx context.Context, _ *maitrev1.GetParityStatusRequest) (*maitrev1.GetParityStatusResponse, error) {
	if s.Parity == nil {
		return nil, errNoParity
	}
	return &maitrev1.GetParityStatusResponse{Status: s.Parity.Status(ctx)}, nil
}

func (s *Service) MakeMirroredDrive(ctx context.Context, req *maitrev1.MakeMirroredDriveRequest) (*maitrev1.MakeMirroredDriveResponse, error) {
	d, err := s.m.MakeMirrored(ctx, req.GetDevices(), req.GetName(), req.GetTags(), req.GetConfirm())
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.MakeMirroredDriveResponse{Drive: d}, nil
}

func (s *Service) ReplaceMirrorDisk(ctx context.Context, req *maitrev1.ReplaceMirrorDiskRequest) (*maitrev1.ReplaceMirrorDiskResponse, error) {
	d, err := s.m.ReplaceMirrorDisk(ctx, req.GetDriveId(), req.GetDevice(), req.GetConfirm())
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%v", err)
	}
	return &maitrev1.ReplaceMirrorDiskResponse{Drive: d}, nil
}

func (s *Service) CopiesNow(ctx context.Context, _ *maitrev1.CopiesNowRequest) (*maitrev1.CopiesNowResponse, error) {
	if s.Copies == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Node keeps no copies")
	}
	go func() { _ = s.Copies.Pass(context.WithoutCancel(ctx)) }()
	return &maitrev1.CopiesNowResponse{}, nil
}
