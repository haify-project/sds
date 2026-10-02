package controller

import (
	"context"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

func (s *Server) CreateZFSPool(ctx context.Context, req *sdspb.CreateZFSPoolRequest) (*sdspb.CreateZFSPoolResponse, error) {
	err := s.storage.CreateZFSPool(ctx, req.Name, req.Node, req.Vdevs)
	if err != nil {
		return &sdspb.CreateZFSPoolResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateZFSPoolResponse{
		Success: true,
		Message: "ZFS pool created successfully",
	}, nil
}

func (s *Server) DeleteZFSPool(ctx context.Context, req *sdspb.DeleteZFSPoolRequest) (*sdspb.DeleteZFSPoolResponse, error) {
	err := s.storage.DeleteZFSPool(ctx, req.Name, req.Node)
	if err != nil {
		return &sdspb.DeleteZFSPoolResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteZFSPoolResponse{
		Success: true,
		Message: "ZFS pool deleted successfully",
	}, nil
}

func (s *Server) ListZFSpools(ctx context.Context, req *sdspb.ListZFSPoolsRequest) (*sdspb.ListZFSPoolsResponse, error) {
	pools, err := s.storage.ListZFSpools(ctx)
	if err != nil {
		return &sdspb.ListZFSPoolsResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbPools []*sdspb.PoolInfo
	for _, p := range pools {
		pbPools = append(pbPools, pbPoolInfo(p))
	}

	return &sdspb.ListZFSPoolsResponse{
		Success: true,
		Message: "ZFS pools listed successfully",
		Pools:   pbPools,
	}, nil
}

func (s *Server) CreateZFSDataset(ctx context.Context, req *sdspb.CreateZFSDatasetRequest) (*sdspb.CreateZFSDatasetResponse, error) {
	err := s.storage.CreateZFSDataset(ctx, req.DatasetPath, req.Node)
	if err != nil {
		return &sdspb.CreateZFSDatasetResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateZFSDatasetResponse{
		Success: true,
		Message: "ZFS dataset created successfully",
	}, nil
}

func (s *Server) CreateZFSVolume(ctx context.Context, req *sdspb.CreateZFSVolumeRequest) (*sdspb.CreateZFSVolumeResponse, error) {
	err := s.storage.CreateZFSThinVolume(ctx, req.PoolName, req.VolumeName, req.Size, req.Node)
	if err != nil {
		return &sdspb.CreateZFSVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateZFSVolumeResponse{
		Success: true,
		Message: "ZFS volume created successfully",
	}, nil
}

func (s *Server) ResizeZFSVolume(ctx context.Context, req *sdspb.ResizeZFSVolumeRequest) (*sdspb.ResizeZFSVolumeResponse, error) {
	err := s.storage.ZFSResizeVolume(ctx, req.VolumePath, req.NewSize, req.Node)
	if err != nil {
		return &sdspb.ResizeZFSVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.ResizeZFSVolumeResponse{
		Success: true,
		Message: "ZFS volume resized successfully",
	}, nil
}

func (s *Server) DeleteZFSDataset(ctx context.Context, req *sdspb.DeleteZFSDatasetRequest) (*sdspb.DeleteZFSDatasetResponse, error) {
	// Use ZFS destroy for both datasets and volumes
	err := s.storage.ZFSDeleteDataset(ctx, req.DatasetPath, req.Node)
	if err != nil {
		return &sdspb.DeleteZFSDatasetResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteZFSDatasetResponse{
		Success: true,
		Message: "ZFS dataset deleted successfully",
	}, nil
}

func (s *Server) CreateZFSSnapshot(ctx context.Context, req *sdspb.CreateZFSSnapshotRequest) (*sdspb.CreateZFSSnapshotResponse, error) {
	err := s.storage.ZFSSnapshot(ctx, req.Dataset, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.CreateZFSSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateZFSSnapshotResponse{
		Success: true,
		Message: "ZFS snapshot created successfully",
	}, nil
}

func (s *Server) DeleteZFSSnapshot(ctx context.Context, req *sdspb.DeleteZFSSnapshotRequest) (*sdspb.DeleteZFSSnapshotResponse, error) {
	err := s.storage.ZFSDeleteSnapshot(ctx, req.Snapshot, req.Node)
	if err != nil {
		return &sdspb.DeleteZFSSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteZFSSnapshotResponse{
		Success: true,
		Message: "ZFS snapshot deleted successfully",
	}, nil
}

func (s *Server) ListZFSSnapshots(ctx context.Context, req *sdspb.ListZFSSnapshotsRequest) (*sdspb.ListZFSSnapshotsResponse, error) {
	snapshots, err := s.storage.ZFSListSnapshots(ctx, req.Dataset, req.Node)
	if err != nil {
		return &sdspb.ListZFSSnapshotsResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbSnapshots []*sdspb.SnapshotInfo
	for _, snap := range snapshots {
		pbSnapshots = append(pbSnapshots, &sdspb.SnapshotInfo{
			Name:      snap.Name,
			Volume:    snap.Volume,
			SizeGb:    snap.SizeGB,
			CreatedAt: snap.CreatedAt,
			Origin:    snap.Origin,
		})
	}

	return &sdspb.ListZFSSnapshotsResponse{
		Success:   true,
		Message:   "ZFS snapshots listed successfully",
		Snapshots: pbSnapshots,
	}, nil
}

func (s *Server) RestoreZFSSnapshot(ctx context.Context, req *sdspb.RestoreZFSSnapshotRequest) (*sdspb.RestoreZFSSnapshotResponse, error) {
	err := s.storage.ZFSRestoreSnapshot(ctx, req.Dataset, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.RestoreZFSSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.RestoreZFSSnapshotResponse{
		Success: true,
		Message: "ZFS snapshot restored successfully",
	}, nil
}

func (s *Server) CloneZFSSnapshot(ctx context.Context, req *sdspb.CloneZFSSnapshotRequest) (*sdspb.CloneZFSSnapshotResponse, error) {
	err := s.storage.ZFSCloneSnapshot(ctx, req.Snapshot, req.ClonePath, req.Node)
	if err != nil {
		return &sdspb.CloneZFSSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CloneZFSSnapshotResponse{
		Success: true,
		Message: "ZFS snapshot cloned successfully",
	}, nil
}
