package controller

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// Resource snapshot RPCs (resource_snapshot.go).

func (s *Server) CreateResourceSnapshot(ctx context.Context, req *haifypb.CreateResourceSnapshotRequest) (*haifypb.CreateResourceSnapshotResponse, error) {
	if err := s.resources.CreateResourceSnapshot(ctx, req.Resource, req.Name); err != nil {
		return &haifypb.CreateResourceSnapshotResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.CreateResourceSnapshotResponse{Success: true,
		Message: fmt.Sprintf("snapshot %s of %s taken on every replica", req.Name, req.Resource)}, nil
}

func (s *Server) ListResourceSnapshots(ctx context.Context, req *haifypb.ListResourceSnapshotsRequest) (*haifypb.ListResourceSnapshotsResponse, error) {
	names, err := s.resources.ListResourceSnapshots(ctx, req.Resource)
	if err != nil {
		return &haifypb.ListResourceSnapshotsResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.ListResourceSnapshotsResponse{Success: true, Names: names}, nil
}

func (s *Server) RollbackResourceSnapshot(ctx context.Context, req *haifypb.RollbackResourceSnapshotRequest) (*haifypb.RollbackResourceSnapshotResponse, error) {
	if err := s.resources.RollbackResourceSnapshot(ctx, req.Resource, req.Name); err != nil {
		return &haifypb.RollbackResourceSnapshotResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.RollbackResourceSnapshotResponse{Success: true,
		Message: fmt.Sprintf("%s rolled back to %s on every replica", req.Resource, req.Name)}, nil
}

func (s *Server) DeleteResourceSnapshot(ctx context.Context, req *haifypb.DeleteResourceSnapshotRequest) (*haifypb.DeleteResourceSnapshotResponse, error) {
	if err := s.resources.DeleteResourceSnapshot(ctx, req.Resource, req.Name); err != nil {
		return &haifypb.DeleteResourceSnapshotResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.DeleteResourceSnapshotResponse{Success: true,
		Message: fmt.Sprintf("snapshot %s of %s deleted", req.Name, req.Resource)}, nil
}
