package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// Off-cluster backup shipping. See pkg/backup for what this is and is not:
// every backup is a FULL image, and only a "completed" one is restorable.

// AddBackupTarget stores (or replaces) a backup repository. The secret in req
// is write-only server-side and is never returned by any other call.
func (c *SDSClient) AddBackupTarget(ctx context.Context, req *sdspb.AddBackupTargetRequest) error {
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
func (c *SDSClient) ListBackupTargets(ctx context.Context) ([]*sdspb.BackupTargetInfo, error) {
	resp, err := c.client.ListBackupTargets(ctx, &sdspb.ListBackupTargetsRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Targets, nil
}

// DeleteBackupTarget removes a backup target definition.
func (c *SDSClient) DeleteBackupTarget(ctx context.Context, name string, force bool) error {
	resp, err := c.client.DeleteBackupTarget(ctx, &sdspb.DeleteBackupTargetRequest{Name: name, Force: force})
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
func (c *SDSClient) CreateBackup(ctx context.Context, resource, target, node string, full bool) (*sdspb.BackupInfo, error) {
	resp, err := c.client.CreateBackup(ctx, &sdspb.CreateBackupRequest{
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
func (c *SDSClient) ListBackups(ctx context.Context, resource, target string) ([]*sdspb.BackupInfo, error) {
	resp, err := c.client.ListBackups(ctx, &sdspb.ListBackupsRequest{Resource: resource, Target: target})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Backups, nil
}

// RestoreBackup writes a completed backup back into a resource.
func (c *SDSClient) RestoreBackup(ctx context.Context, id, resource, node string) (*sdspb.BackupInfo, error) {
	resp, err := c.client.RestoreBackup(ctx, &sdspb.RestoreBackupRequest{
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
func (c *SDSClient) DeleteBackup(ctx context.Context, id, node string, force bool) error {
	resp, err := c.client.DeleteBackup(ctx, &sdspb.DeleteBackupRequest{Id: id, Node: node, Force: force})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ImportBackups rebuilds backup records from the manifests on a target.
func (c *SDSClient) ImportBackups(ctx context.Context, target, node string) (*sdspb.ImportBackupsResponse, error) {
	resp, err := c.client.ImportBackups(ctx, &sdspb.ImportBackupsRequest{Target: target, Node: node})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// CreateBackupSchedule creates or replaces the schedule backing resource up to target.
func (c *SDSClient) CreateBackupSchedule(ctx context.Context, req *sdspb.CreateBackupScheduleRequest) (*sdspb.BackupScheduleInfo, error) {
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
func (c *SDSClient) ListBackupSchedules(ctx context.Context) ([]*sdspb.BackupScheduleInfo, error) {
	resp, err := c.client.ListBackupSchedules(ctx, &sdspb.ListBackupSchedulesRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Schedules, nil
}

// DeleteBackupSchedule stops a schedule; its backups stay.
func (c *SDSClient) DeleteBackupSchedule(ctx context.Context, name string) error {
	resp, err := c.client.DeleteBackupSchedule(ctx, &sdspb.DeleteBackupScheduleRequest{Name: name})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RunBackupSchedule runs a schedule now and waits for the backup.
func (c *SDSClient) RunBackupSchedule(ctx context.Context, name string) (*sdspb.BackupScheduleInfo, error) {
	resp, err := c.client.RunBackupSchedule(ctx, &sdspb.RunBackupScheduleRequest{Name: name})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return resp.Schedule, fmt.Errorf("%s", resp.Message)
	}
	return resp.Schedule, nil
}
