package controller

import (
	"context"
	"fmt"
	"time"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
)

func (s *Server) CreateSnapshot(ctx context.Context, req *sdspb.CreateSnapshotRequest) (*sdspb.CreateSnapshotResponse, error) {
	err := s.snapshots.CreateSnapshot(ctx, req.Volume, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.CreateSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateSnapshotResponse{
		Success: true,
		Message: "Snapshot created successfully",
	}, nil
}

func (s *Server) DeleteSnapshot(ctx context.Context, req *sdspb.DeleteSnapshotRequest) (*sdspb.DeleteSnapshotResponse, error) {
	err := s.snapshots.DeleteSnapshot(ctx, req.Volume, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.DeleteSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteSnapshotResponse{
		Success: true,
		Message: "Snapshot deleted successfully",
	}, nil
}

func (s *Server) RestoreSnapshot(ctx context.Context, req *sdspb.RestoreSnapshotRequest) (*sdspb.RestoreSnapshotResponse, error) {
	err := s.snapshots.RestoreSnapshot(ctx, req.Volume, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.RestoreSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.RestoreSnapshotResponse{
		Success: true,
		Message: "Snapshot restored successfully",
	}, nil
}

// PopulateVolume fills a freshly created, still-empty resource from a source
// block device (a snapshot, or another volume's backing store). It is the
// server side of CSI restore-from-snapshot and volume cloning.
func (s *Server) PopulateVolume(ctx context.Context, req *sdspb.PopulateVolumeRequest) (*sdspb.PopulateVolumeResponse, error) {
	copied, err := s.snapshots.PopulateVolume(ctx, req.GetResource(), req.GetVolumeId(), req.GetSourceDevice(), req.GetNode())
	if err != nil {
		return &sdspb.PopulateVolumeResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.PopulateVolumeResponse{
		Success:     true,
		Message:     "Volume populated successfully",
		BytesCopied: copied,
	}, nil
}

func (s *Server) ListSnapshots(ctx context.Context, req *sdspb.ListSnapshotsRequest) (*sdspb.ListSnapshotsResponse, error) {
	snapshots, err := s.snapshots.ListSnapshots(ctx, req.Volume, req.Node)
	if err != nil {
		return &sdspb.ListSnapshotsResponse{
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

	return &sdspb.ListSnapshotsResponse{
		Success:   true,
		Message:   "Snapshots listed successfully",
		Snapshots: pbSnapshots,
	}, nil
}

func (s *Server) CreateSnapshotSchedule(ctx context.Context, req *sdspb.CreateSnapshotScheduleRequest) (*sdspb.CreateSnapshotScheduleResponse, error) {
	err := s.ctrl.schedules.CreateSchedule(ctx, req.Resource, req.Cron, gfsFromProto(req.Keep), req.Enabled)
	if err != nil {
		return &sdspb.CreateSnapshotScheduleResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.CreateSnapshotScheduleResponse{
		Success: true,
		Message: fmt.Sprintf("Snapshot schedule for %q created", req.Resource),
	}, nil
}

func (s *Server) ListSnapshotSchedules(ctx context.Context, req *sdspb.ListSnapshotSchedulesRequest) (*sdspb.ListSnapshotSchedulesResponse, error) {
	schedules, err := s.ctrl.schedules.ListSchedules(ctx)
	if err != nil {
		return &sdspb.ListSnapshotSchedulesResponse{Success: false, Message: err.Error()}, nil
	}
	now := time.Now()
	var out []*sdspb.SnapshotScheduleInfo
	for _, sc := range schedules {
		info := &sdspb.SnapshotScheduleInfo{
			Name:     sc.Name,
			Resource: sc.Resource,
			Cron:     sc.Cron,
			Enabled:  sc.Enabled,
			Keep:     gfsToProto(sc.Keep),
		}
		if !sc.LastRun.IsZero() {
			info.LastRun = sc.LastRun.UTC().Format(time.RFC3339)
		}
		if sc.Enabled {
			if next := NextRun(sc.Cron, now); !next.IsZero() {
				info.NextRun = next.UTC().Format(time.RFC3339)
			}
		}
		out = append(out, info)
	}
	return &sdspb.ListSnapshotSchedulesResponse{
		Success:   true,
		Message:   "Snapshot schedules listed successfully",
		Schedules: out,
	}, nil
}

func (s *Server) DeleteSnapshotSchedule(ctx context.Context, req *sdspb.DeleteSnapshotScheduleRequest) (*sdspb.DeleteSnapshotScheduleResponse, error) {
	if err := s.ctrl.schedules.DeleteSchedule(ctx, req.Name); err != nil {
		return &sdspb.DeleteSnapshotScheduleResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.DeleteSnapshotScheduleResponse{
		Success: true,
		Message: fmt.Sprintf("Snapshot schedule %q deleted", req.Name),
	}, nil
}

func gfsFromProto(p *sdspb.GFSRetention) database.GFSPolicy {
	if p == nil {
		return database.GFSPolicy{}
	}
	return database.GFSPolicy{
		Hourly:  int(p.Hourly),
		Daily:   int(p.Daily),
		Weekly:  int(p.Weekly),
		Monthly: int(p.Monthly),
		Yearly:  int(p.Yearly),
	}
}

func gfsToProto(p database.GFSPolicy) *sdspb.GFSRetention {
	return &sdspb.GFSRetention{
		Hourly:  int32(p.Hourly),
		Daily:   int32(p.Daily),
		Weekly:  int32(p.Weekly),
		Monthly: int32(p.Monthly),
		Yearly:  int32(p.Yearly),
	}
}

func (s *Server) CreateLvmSnapshot(ctx context.Context, req *sdspb.CreateLvmSnapshotRequest) (*sdspb.CreateLvmSnapshotResponse, error) {
	err := s.storage.CreateLvmSnapshot(ctx, req.Resource, req.LvName, req.SnapshotName, req.Node, req.Size)
	if err != nil {
		return &sdspb.CreateLvmSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateLvmSnapshotResponse{
		Success: true,
		Message: "LVM snapshot created successfully",
	}, nil
}

func (s *Server) DeleteLvmSnapshot(ctx context.Context, req *sdspb.DeleteLvmSnapshotRequest) (*sdspb.DeleteLvmSnapshotResponse, error) {
	err := s.storage.DeleteLvmSnapshot(ctx, req.LvName, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.DeleteLvmSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteLvmSnapshotResponse{
		Success: true,
		Message: "LVM snapshot deleted successfully",
	}, nil
}

func (s *Server) ListLvmSnapshots(ctx context.Context, req *sdspb.ListLvmSnapshotsRequest) (*sdspb.ListLvmSnapshotsResponse, error) {
	snapshots, err := s.storage.ListLvmSnapshots(ctx, req.LvName, req.Node, req.Resource)
	if err != nil {
		return &sdspb.ListLvmSnapshotsResponse{
			Success:   false,
			Message:   err.Error(),
			Snapshots: nil,
		}, nil
	}
	// Convert to proto SnapshotInfo
	var protoSnapshots []*sdspb.SnapshotInfo
	for _, snap := range snapshots {
		protoSnapshots = append(protoSnapshots, &sdspb.SnapshotInfo{
			Name:      snap.Name,
			Volume:    snap.Volume,
			SizeGb:    snap.SizeGB,
			CreatedAt: snap.CreatedAt,
			Origin:    snap.Origin,
		})
	}
	return &sdspb.ListLvmSnapshotsResponse{
		Success:   true,
		Message:   "LVM snapshots listed successfully",
		Snapshots: protoSnapshots,
	}, nil
}

func (s *Server) RestoreLvmSnapshot(ctx context.Context, req *sdspb.RestoreLvmSnapshotRequest) (*sdspb.RestoreLvmSnapshotResponse, error) {
	err := s.storage.RestoreLvmSnapshot(ctx, req.LvName, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.RestoreLvmSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.RestoreLvmSnapshotResponse{
		Success: true,
		Message: "LVM snapshot restored successfully",
	}, nil
}
