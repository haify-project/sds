package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// Storage upkeep: trimming, disks, and storage jobs.

// TrimPools trims the DRBD-backed filesystems on node, or on every node.
func (c *SDSClient) TrimPools(ctx context.Context, node string) (*sdspb.TrimPoolsResponse, error) {
	return c.client.TrimPools(ctx, &sdspb.TrimPoolsRequest{Node: node})
}

// ListPoolDisks lists the disks under pools, with their health.
func (c *SDSClient) ListPoolDisks(ctx context.Context, pool, node string) ([]*sdspb.PoolDiskInfo, error) {
	resp, err := c.client.ListPoolDisks(ctx, &sdspb.ListPoolDisksRequest{Pool: pool, Node: node})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Disks, nil
}

// RemovePoolDisk starts moving a disk's data off it and taking it out of the pool.
func (c *SDSClient) RemovePoolDisk(ctx context.Context, pool, node, disk string) (*sdspb.StorageJobResponse, error) {
	return jobResult(c.client.RemovePoolDisk(ctx, &sdspb.RemovePoolDiskRequest{Pool: pool, Node: node, Disk: disk}))
}

// ReplacePoolDisk starts moving a disk's data to a new disk.
func (c *SDSClient) ReplacePoolDisk(ctx context.Context, pool, node, oldDisk, newDisk string) (*sdspb.StorageJobResponse, error) {
	return jobResult(c.client.ReplacePoolDisk(ctx, &sdspb.ReplacePoolDiskRequest{Pool: pool, Node: node, OldDisk: oldDisk, NewDisk: newDisk}))
}

// MoveVolume starts moving a resource's volume to another pool.
func (c *SDSClient) MoveVolume(ctx context.Context, resource string, volumeID int32, pool string) (*sdspb.StorageJobResponse, error) {
	return jobResult(c.client.MoveVolume(ctx, &sdspb.MoveVolumeRequest{Resource: resource, VolumeId: volumeID, Pool: pool}))
}

// ListStorageJobs lists running (and optionally finished) storage jobs.
func (c *SDSClient) ListStorageJobs(ctx context.Context, includeFinished bool) ([]*sdspb.StorageJobInfo, error) {
	resp, err := c.client.ListStorageJobs(ctx, &sdspb.ListStorageJobsRequest{IncludeFinished: includeFinished})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Jobs, nil
}

func jobResult(resp *sdspb.StorageJobResponse, err error) (*sdspb.StorageJobResponse, error) {
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}
