package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// CreateSnapshot creates a snapshot
func (c *SDSClient) CreateSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	req := &sdspb.CreateSnapshotRequest{
		Volume:       volume,
		SnapshotName: snapshotName,
		Node:         node,
	}

	resp, err := c.client.CreateSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// DeleteSnapshot deletes a snapshot
func (c *SDSClient) DeleteSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	req := &sdspb.DeleteSnapshotRequest{
		Volume:       volume,
		SnapshotName: snapshotName,
		Node:         node,
	}

	resp, err := c.client.DeleteSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// ListSnapshots lists snapshots for a volume
func (c *SDSClient) ListSnapshots(ctx context.Context, volume, node string) ([]*sdspb.SnapshotInfo, error) {
	req := &sdspb.ListSnapshotsRequest{
		Volume: volume,
		Node:   node,
	}

	resp, err := c.client.ListSnapshots(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Snapshots, nil
}

// CreateSnapshotSchedule creates a cron-driven snapshot schedule with GFS
// retention, keeping the lock of any schedule it replaces.
func (c *SDSClient) CreateSnapshotSchedule(ctx context.Context, resource, cron string, keep *sdspb.GFSRetention, enabled bool) error {
	return c.CreateSnapshotScheduleLocked(ctx, resource, cron, keep, enabled, nil)
}

// CreateSnapshotScheduleLocked is CreateSnapshotSchedule setting how many days
// the schedule locks its snapshots; nil keeps the current lock.
func (c *SDSClient) CreateSnapshotScheduleLocked(ctx context.Context, resource, cron string, keep *sdspb.GFSRetention, enabled bool, lockDays *uint32) error {
	resp, err := c.client.CreateSnapshotSchedule(ctx, &sdspb.CreateSnapshotScheduleRequest{
		Resource: resource,
		Cron:     cron,
		Keep:     keep,
		Enabled:  enabled,
		LockDays: lockDays,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListSnapshotSchedules returns all snapshot schedules.
func (c *SDSClient) ListSnapshotSchedules(ctx context.Context) ([]*sdspb.SnapshotScheduleInfo, error) {
	resp, err := c.client.ListSnapshotSchedules(ctx, &sdspb.ListSnapshotSchedulesRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Schedules, nil
}

// DeleteSnapshotSchedule removes a snapshot schedule by name.
func (c *SDSClient) DeleteSnapshotSchedule(ctx context.Context, name string) error {
	resp, err := c.client.DeleteSnapshotSchedule(ctx, &sdspb.DeleteSnapshotScheduleRequest{Name: name})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RestoreSnapshot restores a snapshot to its source volume
// PopulateVolume copies sourceDevice (an LVM/ZFS snapshot, or another volume's
// backing store) into an already-created, still-empty resource. It backs CSI
// restore-from-snapshot and volume cloning; unlike RestoreSnapshot it fills a
// different, new volume rather than merging into the source's own origin.
func (c *SDSClient) PopulateVolume(ctx context.Context, resource string, volumeID uint32, sourceDevice, node string) (uint64, error) {
	resp, err := c.client.PopulateVolume(ctx, &sdspb.PopulateVolumeRequest{
		Resource:     resource,
		VolumeId:     volumeID,
		SourceDevice: sourceDevice,
		Node:         node,
	})
	if err != nil {
		return 0, err
	}
	if !resp.Success {
		return 0, fmt.Errorf("%s", resp.Message)
	}
	return resp.BytesCopied, nil
}

func (c *SDSClient) RestoreSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	req := &sdspb.RestoreSnapshotRequest{
		Volume:       volume,
		SnapshotName: snapshotName,
		Node:         node,
	}

	resp, err := c.client.RestoreSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// CreateLvmSnapshot creates an LVM snapshot
func (c *SDSClient) CreateLvmSnapshot(ctx context.Context, pool, lvName, snapshotName, node, size string) error {
	req := &sdspb.CreateLvmSnapshotRequest{
		Resource:     pool, // Mapped to VG Name
		LvName:       lvName,
		SnapshotName: snapshotName,
		Node:         node,
		Size:         size,
	}

	resp, err := c.client.CreateLvmSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// DeleteLvmSnapshot deletes an LVM snapshot
func (c *SDSClient) DeleteLvmSnapshot(ctx context.Context, pool, snapshotName, node string) error {
	req := &sdspb.DeleteLvmSnapshotRequest{
		LvName:       pool, // Mapped to VG Name
		SnapshotName: snapshotName,
		Node:         node,
	}

	resp, err := c.client.DeleteLvmSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// ListLvmSnapshots lists LVM snapshots in a pool (VG). resource narrows the
// result to that DRBD resource's volumes; empty lists the whole pool.
func (c *SDSClient) ListLvmSnapshots(ctx context.Context, pool, node, resource string) ([]*sdspb.SnapshotInfo, error) {
	req := &sdspb.ListLvmSnapshotsRequest{
		LvName:   pool, // the pool is a volume group at the LVM level
		Node:     node,
		Resource: resource,
	}

	resp, err := c.client.ListLvmSnapshots(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Snapshots, nil
}

// RestoreLvmSnapshot restores an LVM snapshot
func (c *SDSClient) RestoreLvmSnapshot(ctx context.Context, pool, snapshotName, node string) error {
	req := &sdspb.RestoreLvmSnapshotRequest{
		LvName:       pool, // Mapped to VG Name
		SnapshotName: snapshotName,
		Node:         node,
	}

	resp, err := c.client.RestoreLvmSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}
