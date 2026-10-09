package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// Off-cluster backup shipping. See pkg/backup for what this is and is not:
// every backup is a FULL image, and only a "completed" one is restorable.

// AddBackupTarget stores (or replaces) a backup repository. The secret in req
// is write-only server-side and is never returned by any other call.
func (c *HaifyClient) AddBackupTarget(ctx context.Context, req *haifypb.AddBackupTargetRequest) error {
	resp, err := c.client.AddBackupTarget(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListBackupTargets returns every backup target, without their secrets.
func (c *HaifyClient) ListBackupTargets(ctx context.Context) ([]*haifypb.BackupTargetInfo, error) {
	resp, err := c.client.ListBackupTargets(ctx, &haifypb.ListBackupTargetsRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Targets, nil
}

// DeleteBackupTarget removes a backup target definition.
func (c *HaifyClient) DeleteBackupTarget(ctx context.Context, name string, force bool) error {
	resp, err := c.client.DeleteBackupTarget(ctx, &haifypb.DeleteBackupTargetRequest{Name: name, Force: force})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// CreateBackup ships a crash-consistent copy of a resource to a target:
// incremental on the last backup when it can be, full when full is set or it
// cannot.
func (c *HaifyClient) CreateBackup(ctx context.Context, resource, target, node string, full bool) (*haifypb.BackupInfo, error) {
	resp, err := c.client.CreateBackup(ctx, &haifypb.CreateBackupRequest{
		Resource: resource, Target: target, Node: node, Full: full,
	})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Backup, nil
}

// ListBackups returns backup records, newest first. Empty filters match all.
func (c *HaifyClient) ListBackups(ctx context.Context, resource, target string) ([]*haifypb.BackupInfo, error) {
	resp, err := c.client.ListBackups(ctx, &haifypb.ListBackupsRequest{Resource: resource, Target: target})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Backups, nil
}

// RestoreBackup writes a completed backup back into a resource.
func (c *HaifyClient) RestoreBackup(ctx context.Context, id, resource, node string) (*haifypb.BackupInfo, error) {
	resp, err := c.client.RestoreBackup(ctx, &haifypb.RestoreBackupRequest{
		Id: id, Resource: resource, Node: node,
	})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Backup, nil
}

// DeleteBackup removes a backup's objects and its record.
func (c *HaifyClient) DeleteBackup(ctx context.Context, id, node string, force bool) error {
	resp, err := c.client.DeleteBackup(ctx, &haifypb.DeleteBackupRequest{Id: id, Node: node, Force: force})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ImportBackups rebuilds backup records from the manifests on a target.
func (c *HaifyClient) ImportBackups(ctx context.Context, target, node string) (*haifypb.ImportBackupsResponse, error) {
	return c.ImportBackupsAsOf(ctx, target, node, "")
}

// ImportBackupsAsOf is ImportBackups reading the target as it was at asOf
// (RFC3339); empty reads it as it is.
func (c *HaifyClient) ImportBackupsAsOf(ctx context.Context, target, node, asOf string) (*haifypb.ImportBackupsResponse, error) {
	resp, err := c.client.ImportBackups(ctx, &haifypb.ImportBackupsRequest{Target: target, Node: node, AsOf: asOf})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// CreateBackupSchedule creates or replaces the schedule backing resource up to target.
func (c *HaifyClient) CreateBackupSchedule(ctx context.Context, req *haifypb.CreateBackupScheduleRequest) (*haifypb.BackupScheduleInfo, error) {
	resp, err := c.client.CreateBackupSchedule(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Schedule, nil
}

// ListBackupSchedules returns every backup schedule.
func (c *HaifyClient) ListBackupSchedules(ctx context.Context) ([]*haifypb.BackupScheduleInfo, error) {
	resp, err := c.client.ListBackupSchedules(ctx, &haifypb.ListBackupSchedulesRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Schedules, nil
}

// DeleteBackupSchedule stops a schedule; its backups stay.
func (c *HaifyClient) DeleteBackupSchedule(ctx context.Context, name string) error {
	resp, err := c.client.DeleteBackupSchedule(ctx, &haifypb.DeleteBackupScheduleRequest{Name: name})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RunBackupSchedule runs a schedule now and waits for the backup.
func (c *HaifyClient) RunBackupSchedule(ctx context.Context, name string) (*haifypb.BackupScheduleInfo, error) {
	resp, err := c.client.RunBackupSchedule(ctx, &haifypb.RunBackupScheduleRequest{Name: name})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return resp.Schedule, fmt.Errorf("%s", resp.Message)
	}
	return resp.Schedule, nil
}
