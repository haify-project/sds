package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
)

// RPCs for scheduled backups and for importing backups from a target.

func (s *Server) CreateBackupSchedule(ctx context.Context, req *sdspb.CreateBackupScheduleRequest) (*sdspb.CreateBackupScheduleResponse, error) {
	sc, err := s.ctrl.schedules.CreateBackupSchedule(ctx, req.Resource, req.Target, req.Cron, gfsFromProto(req.Keep), req.Enabled)
	if err != nil {
		return &sdspb.CreateBackupScheduleResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.CreateBackupScheduleResponse{
		Success:  true,
		Message:  fmt.Sprintf("Backup schedule %s saved", sc.Name),
		Schedule: backupScheduleToProto(sc, time.Now()),
	}, nil
}

func (s *Server) ListBackupSchedules(ctx context.Context, _ *sdspb.ListBackupSchedulesRequest) (*sdspb.ListBackupSchedulesResponse, error) {
	schedules, err := s.ctrl.schedules.ListBackupSchedules(ctx)
	if err != nil {
		return &sdspb.ListBackupSchedulesResponse{Success: false, Message: err.Error()}, nil
	}
	now := time.Now()
	out := make([]*sdspb.BackupScheduleInfo, 0, len(schedules))
	for _, sc := range schedules {
		out = append(out, backupScheduleToProto(sc, now))
	}
	return &sdspb.ListBackupSchedulesResponse{Success: true, Message: "Backup schedules listed", Schedules: out}, nil
}

func (s *Server) DeleteBackupSchedule(ctx context.Context, req *sdspb.DeleteBackupScheduleRequest) (*sdspb.DeleteBackupScheduleResponse, error) {
	if err := s.ctrl.schedules.DeleteBackupSchedule(ctx, req.Name); err != nil {
		return &sdspb.DeleteBackupScheduleResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.DeleteBackupScheduleResponse{Success: true, Message: fmt.Sprintf("Backup schedule %s deleted", req.Name)}, nil
}

func (s *Server) RunBackupSchedule(ctx context.Context, req *sdspb.RunBackupScheduleRequest) (*sdspb.RunBackupScheduleResponse, error) {
	sc, err := s.ctrl.schedules.RunBackupSchedule(ctx, req.Name)
	if err != nil {
		return &sdspb.RunBackupScheduleResponse{Success: false, Message: err.Error()}, nil
	}
	if sc.LastError != "" {
		return &sdspb.RunBackupScheduleResponse{
			Success: false, Message: "backup failed: " + sc.LastError, Schedule: backupScheduleToProto(sc, time.Now()),
		}, nil
	}
	return &sdspb.RunBackupScheduleResponse{
		Success: true, Message: fmt.Sprintf("Backup %s completed", sc.LastBackup), Schedule: backupScheduleToProto(sc, time.Now()),
	}, nil
}

func (s *Server) ImportBackups(ctx context.Context, req *sdspb.ImportBackupsRequest) (*sdspb.ImportBackupsResponse, error) {
	res, err := s.ctrl.backups.ImportBackups(ctx, req.Target, req.Node)
	if err != nil {
		return &sdspb.ImportBackupsResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.ImportBackupsResponse{
		Success:  true,
		Message:  fmt.Sprintf("Imported %d backup(s) from %s; %d skipped", len(res.Imported), req.Target, len(res.Skipped)),
		Imported: res.Imported,
		Skipped:  res.Skipped,
	}, nil
}

func backupScheduleToProto(sc *database.BackupSchedule, now time.Time) *sdspb.BackupScheduleInfo {
	info := &sdspb.BackupScheduleInfo{
		Name: sc.Name, Resource: sc.Resource, Target: sc.Target, Cron: sc.Cron, Enabled: sc.Enabled,
		Keep: gfsToProto(sc.Keep), LastBackup: sc.LastBackup, LastError: strings.TrimSpace(sc.LastError),
	}
	if !sc.LastRun.IsZero() {
		info.LastRun = sc.LastRun.UTC().Format(time.RFC3339)
	}
	if sc.Enabled {
		if next := NextRun(sc.Cron, now); !next.IsZero() {
			info.NextRun = next.UTC().Format(time.RFC3339)
		}
	}
	return info
}
