package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// CreateZFSPool creates a ZFS pool with the OpenZFS default compression.
func (c *SDSClient) CreateZFSPool(ctx context.Context, name, node string, vdevs []string) error {
	return c.CreateZFSPoolOptions(ctx, name, node, vdevs, "", false)
}

// CreateZFSPoolOptions creates a ZFS pool with the given compression algorithm
// (empty: the OpenZFS default) and deduplication.
func (c *SDSClient) CreateZFSPoolOptions(ctx context.Context, name, node string, vdevs []string, compression string, dedup bool) error {
	req := &sdspb.CreateZFSPoolRequest{
		Name:        name,
		Node:        node,
		Vdevs:       vdevs,
		Compression: compression,
		Dedup:       dedup,
	}

	resp, err := c.client.CreateZFSPool(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// DeleteZFSPool deletes a ZFS pool
func (c *SDSClient) DeleteZFSPool(ctx context.Context, name, node string) error {
	req := &sdspb.DeleteZFSPoolRequest{
		Name: name,
		Node: node,
	}

	resp, err := c.client.DeleteZFSPool(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// ListZFSpools lists all ZFS pools
func (c *SDSClient) ListZFSpools(ctx context.Context) ([]*sdspb.PoolInfo, error) {
	req := &sdspb.ListZFSPoolsRequest{}

	resp, err := c.client.ListZFSpools(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Pools, nil
}

// CreateZFSDataset creates a ZFS dataset
func (c *SDSClient) CreateZFSDataset(ctx context.Context, datasetPath, node string) error {
	req := &sdspb.CreateZFSDatasetRequest{
		DatasetPath: datasetPath,
		Node:        node,
	}

	resp, err := c.client.CreateZFSDataset(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// DeleteZFSDataset deletes a ZFS dataset or volume
func (c *SDSClient) DeleteZFSDataset(ctx context.Context, datasetPath, node string) error {
	req := &sdspb.DeleteZFSDatasetRequest{
		DatasetPath: datasetPath,
		Node:        node,
	}

	resp, err := c.client.DeleteZFSDataset(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// CreateZFSVolume creates a ZFS volume
func (c *SDSClient) CreateZFSVolume(ctx context.Context, poolName, volumeName, size, node string) error {
	req := &sdspb.CreateZFSVolumeRequest{
		PoolName:   poolName,
		VolumeName: volumeName,
		Size:       size,
		Node:       node,
	}

	resp, err := c.client.CreateZFSVolume(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// ResizeZFSVolume resizes a ZFS volume
func (c *SDSClient) ResizeZFSVolume(ctx context.Context, volumePath, newSize, node string) error {
	req := &sdspb.ResizeZFSVolumeRequest{
		VolumePath: volumePath,
		NewSize:    newSize,
		Node:       node,
	}

	resp, err := c.client.ResizeZFSVolume(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// CreateZFSSnapshot creates a ZFS snapshot
func (c *SDSClient) CreateZFSSnapshot(ctx context.Context, dataset, snapshotName, node string) error {
	req := &sdspb.CreateZFSSnapshotRequest{
		Dataset:      dataset,
		SnapshotName: snapshotName,
		Node:         node,
	}

	resp, err := c.client.CreateZFSSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// DeleteZFSSnapshot deletes a ZFS snapshot
func (c *SDSClient) DeleteZFSSnapshot(ctx context.Context, snapshot, node string) error {
	req := &sdspb.DeleteZFSSnapshotRequest{
		Snapshot: snapshot,
		Node:     node,
	}

	resp, err := c.client.DeleteZFSSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// ListZFSSnapshots lists ZFS snapshots
func (c *SDSClient) ListZFSSnapshots(ctx context.Context, dataset, node string) ([]*sdspb.SnapshotInfo, error) {
	req := &sdspb.ListZFSSnapshotsRequest{
		Dataset: dataset,
		Node:    node,
	}

	resp, err := c.client.ListZFSSnapshots(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Snapshots, nil
}

// RestoreZFSSnapshot restores a ZFS snapshot
func (c *SDSClient) RestoreZFSSnapshot(ctx context.Context, dataset, snapshotName, node string) error {
	req := &sdspb.RestoreZFSSnapshotRequest{
		Dataset:      dataset,
		SnapshotName: snapshotName,
		Node:         node,
	}

	resp, err := c.client.RestoreZFSSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// CloneZFSSnapshot clones a ZFS snapshot
func (c *SDSClient) CloneZFSSnapshot(ctx context.Context, snapshot, clonePath, node string) error {
	req := &sdspb.CloneZFSSnapshotRequest{
		Snapshot:  snapshot,
		ClonePath: clonePath,
		Node:      node,
	}

	resp, err := c.client.CloneZFSSnapshot(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}
