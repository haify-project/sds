package client

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
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

// CreateBackup ships a full, crash-consistent copy of a resource to a target.
func (c *SDSClient) CreateBackup(ctx context.Context, resource, target, node string) (*sdspb.BackupInfo, error) {
	resp, err := c.client.CreateBackup(ctx, &sdspb.CreateBackupRequest{
		Resource: resource, Target: target, Node: node,
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
