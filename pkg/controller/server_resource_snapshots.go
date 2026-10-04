package controller

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// Resource snapshot RPCs (resource_snapshot.go).

func (s *Server) CreateResourceSnapshot(ctx context.Context, req *sdspb.CreateResourceSnapshotRequest) (*sdspb.CreateResourceSnapshotResponse, error) {
	if err := s.resources.CreateResourceSnapshot(ctx, req.Resource, req.Name); err != nil {
		return &sdspb.CreateResourceSnapshotResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.CreateResourceSnapshotResponse{Success: true,
		Message: fmt.Sprintf("snapshot %s of %s taken on every replica", req.Name, req.Resource)}, nil
}

func (s *Server) ListResourceSnapshots(ctx context.Context, req *sdspb.ListResourceSnapshotsRequest) (*sdspb.ListResourceSnapshotsResponse, error) {
	names, err := s.resources.ListResourceSnapshots(ctx, req.Resource)
	if err != nil {
		return &sdspb.ListResourceSnapshotsResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.ListResourceSnapshotsResponse{Success: true, Names: names}, nil
}

func (s *Server) RollbackResourceSnapshot(ctx context.Context, req *sdspb.RollbackResourceSnapshotRequest) (*sdspb.RollbackResourceSnapshotResponse, error) {
	if err := s.resources.RollbackResourceSnapshot(ctx, req.Resource, req.Name); err != nil {
		return &sdspb.RollbackResourceSnapshotResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.RollbackResourceSnapshotResponse{Success: true,
		Message: fmt.Sprintf("%s rolled back to %s on every replica", req.Resource, req.Name)}, nil
}

func (s *Server) DeleteResourceSnapshot(ctx context.Context, req *sdspb.DeleteResourceSnapshotRequest) (*sdspb.DeleteResourceSnapshotResponse, error) {
	if err := s.resources.DeleteResourceSnapshot(ctx, req.Resource, req.Name); err != nil {
		return &sdspb.DeleteResourceSnapshotResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.DeleteResourceSnapshotResponse{Success: true,
		Message: fmt.Sprintf("snapshot %s of %s deleted", req.Name, req.Resource)}, nil
}
