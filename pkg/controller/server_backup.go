package controller

import (
	"context"
	"fmt"
	"time"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/backup"
	"github.com/haify-project/haify/pkg/database"
)

// ==================== BACKUP OPERATIONS ====================
//
// The RPC surface for off-cluster backup shipping. Two rules shape it:
// a target's secret is write-only (it enters through AddBackupTarget and is
// never rendered back), and a backup is only reported as restorable in the
// "completed" state.

func (s *Server) AddBackupTarget(ctx context.Context, req *haifypb.AddBackupTargetRequest) (*haifypb.AddBackupTargetResponse, error) {
	kind, err := backup.ParseKind(req.Kind)
	if err != nil {
		return &haifypb.AddBackupTargetResponse{Success: false, Message: err.Error()}, nil
	}
	mode, err := backup.ParseLockMode(req.LockMode)
	if err != nil {
		return &haifypb.AddBackupTargetResponse{Success: false, Message: err.Error()}, nil
	}
	spec := backup.TargetSpec{
		Name: req.Name, Kind: kind, Prefix: req.Prefix,
		Bucket: req.Bucket, Endpoint: req.Endpoint, Region: req.Region,
		Host: req.Host, Share: req.Share,
		User: req.User, Secret: req.Secret, SecretIsObscured: req.SecretObscured,
		LockMode: mode, LockDays: int(req.LockDays), FullEveryDays: int(req.FullEveryDays),
	}
	if err := s.ctrl.backups.AddTarget(ctx, spec); err != nil {
		return &haifypb.AddBackupTargetResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.AddBackupTargetResponse{
		Success: true,
		Message: fmt.Sprintf("Backup target %q saved (%s)", spec.Name, spec.Describe()),
	}, nil
}

func (s *Server) ListBackupTargets(ctx context.Context, req *haifypb.ListBackupTargetsRequest) (*haifypb.ListBackupTargetsResponse, error) {
	targets, err := s.ctrl.backups.ListTargets(ctx)
	if err != nil {
		return &haifypb.ListBackupTargetsResponse{Success: false, Message: err.Error()}, nil
	}
	out := make([]*haifypb.BackupTargetInfo, 0, len(targets))
	for _, t := range targets {
		out = append(out, &haifypb.BackupTargetInfo{
			Name: t.Name, Kind: string(t.Kind), Prefix: t.Prefix,
			Bucket: t.Bucket, Endpoint: t.Endpoint, Region: t.Region,
			Host: t.Host, Share: t.Share, User: t.User,
			Description: t.Describe(),
			LockMode:    string(t.LockMode), LockDays: uint32(t.LockDays), FullEveryDays: uint32(t.FullEveryDays),
		})
	}
	return &haifypb.ListBackupTargetsResponse{
		Success: true, Message: "Backup targets listed successfully", Targets: out,
	}, nil
}

func (s *Server) DeleteBackupTarget(ctx context.Context, req *haifypb.DeleteBackupTargetRequest) (*haifypb.DeleteBackupTargetResponse, error) {
	if err := s.ctrl.backups.DeleteTarget(ctx, req.Name, req.Force); err != nil {
		return &haifypb.DeleteBackupTargetResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.DeleteBackupTargetResponse{
		Success: true, Message: fmt.Sprintf("Backup target %q deleted", req.Name),
	}, nil
}

func (s *Server) CreateBackup(ctx context.Context, req *haifypb.CreateBackupRequest) (*haifypb.CreateBackupResponse, error) {
	rec, err := s.ctrl.backups.CreateBackup(ctx, req.Resource, req.Target, req.Node, req.Full)
	if err != nil {
		return &haifypb.CreateBackupResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.CreateBackupResponse{
		Success: true,
		Message: backupSummary(rec),
		Backup:  backupToProto(rec),
	}, nil
}

func (s *Server) ListBackups(ctx context.Context, req *haifypb.ListBackupsRequest) (*haifypb.ListBackupsResponse, error) {
	backups, err := s.ctrl.backups.ListBackups(ctx, req.Resource, req.Target)
	if err != nil {
		return &haifypb.ListBackupsResponse{Success: false, Message: err.Error()}, nil
	}
	out := make([]*haifypb.BackupInfo, 0, len(backups))
	for _, b := range backups {
		out = append(out, backupToProto(b))
	}
	return &haifypb.ListBackupsResponse{
		Success: true, Message: "Backups listed successfully", Backups: out,
	}, nil
}

func (s *Server) RestoreBackup(ctx context.Context, req *haifypb.RestoreBackupRequest) (*haifypb.RestoreBackupResponse, error) {
	rec, err := s.ctrl.backups.RestoreBackup(ctx, req.Id, req.Resource, req.Node)
	if err != nil {
		return &haifypb.RestoreBackupResponse{Success: false, Message: err.Error()}, nil
	}
	target := req.Resource
	if target == "" {
		target = rec.Resource
	}
	return &haifypb.RestoreBackupResponse{
		Success: true,
		Message: fmt.Sprintf("Backup %s restored into %q (%s)", rec.ID, target, formatBytes(rec.TotalBytes)),
		Backup:  backupToProto(rec),
	}, nil
}

func (s *Server) DeleteBackup(ctx context.Context, req *haifypb.DeleteBackupRequest) (*haifypb.DeleteBackupResponse, error) {
	if err := s.ctrl.backups.DeleteBackup(ctx, req.Id, req.Node, req.Force); err != nil {
		return &haifypb.DeleteBackupResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.DeleteBackupResponse{
		Success: true, Message: fmt.Sprintf("Backup %s deleted", req.Id),
	}, nil
}

// backupToProto renders a backup record for the API.
func backupToProto(b *database.Backup) *haifypb.BackupInfo {
	if b == nil {
		return nil
	}
	info := &haifypb.BackupInfo{
		Id: b.ID, Resource: b.Resource, Target: b.Target, Node: b.Node,
		Backend: b.Backend, State: b.State, Error: b.Error, Prefix: b.Prefix,
		TotalBytes: b.TotalBytes, Kind: b.Kind, Parent: b.Parent, Schedule: b.Schedule,
	}
	if info.Kind == "" {
		info.Kind = database.BackupKindFull
	}
	if !b.StartedAt.IsZero() {
		info.StartedAt = b.StartedAt.UTC().Format(time.RFC3339)
	}
	if !b.FinishedAt.IsZero() {
		info.FinishedAt = b.FinishedAt.UTC().Format(time.RFC3339)
	}
	info.LockMode = b.LockMode
	if !b.RetainUntil.IsZero() {
		info.RetainUntil = b.RetainUntil.UTC().Format(time.RFC3339)
	}
	if !b.ReadAt.IsZero() {
		info.ReadAt = b.ReadAt.UTC().Format(time.RFC3339)
	}
	for _, v := range b.Volumes {
		info.Volumes = append(info.Volumes, &haifypb.BackupVolumeInfo{
			VolumeId: v.VolumeID, Object: v.Object, Bytes: v.Bytes,
			Pool: v.Pool, BackingVolume: v.BackingVolume,
			Ranges: v.Ranges, ChangedBytes: v.ChangedBytes,
		})
	}
	return info
}

// backupSummary says what a finished backup holds.
func backupSummary(rec *database.Backup) string {
	if rec.Kind != database.BackupKindIncremental {
		return fmt.Sprintf("Backup %s of %q completed: full, %s", rec.ID, rec.Resource, formatBytes(rec.TotalBytes))
	}
	var changed uint64
	for _, v := range rec.Volumes {
		changed += v.ChangedBytes
	}
	return fmt.Sprintf("Backup %s of %q completed: incremental on %s, %s changed of %s",
		rec.ID, rec.Resource, rec.Parent, formatBytes(changed), formatBytes(rec.TotalBytes))
}
