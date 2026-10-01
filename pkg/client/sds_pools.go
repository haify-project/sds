package client

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// CreatePool creates a storage pool
func (c *SDSClient) CreatePool(ctx context.Context, name, poolType, node string, disks []string, sizeGB uint64) error {
	req := &sdspb.CreatePoolRequest{
		Name:   name,
		Type:   poolType,
		Node:   node,
		Disks:  disks,
		SizeGb: sizeGB,
	}

	resp, err := c.client.CreatePool(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// GetPool gets pool information
func (c *SDSClient) GetPool(ctx context.Context, name, node string) (*sdspb.PoolInfo, error) {
	req := &sdspb.GetPoolRequest{
		Name: name,
		Node: node,
	}

	resp, err := c.client.GetPool(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Pool, nil
}

// ListPools lists all pools
func (c *SDSClient) ListPools(ctx context.Context) ([]*sdspb.PoolInfo, error) {
	req := &sdspb.ListPoolsRequest{}

	resp, err := c.client.ListPools(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Pools, nil
}

// AddDiskToPool adds a disk to a pool
func (c *SDSClient) AddDiskToPool(ctx context.Context, pool, disk, node string) error {
	req := &sdspb.AddDiskToPoolRequest{
		Pool: pool,
		Disk: disk,
		Node: node,
	}

	resp, err := c.client.AddDiskToPool(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// DeletePool deletes a storage pool
func (c *SDSClient) DeletePool(ctx context.Context, pool, node string) error {
	req := &sdspb.DeletePoolRequest{
		Name: pool,
		Node: node,
	}

	resp, err := c.client.DeletePool(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// ConvertPoolToThin rebuilds one node's LVM pool as a thin pool, in place.
func (c *SDSClient) ConvertPoolToThin(ctx context.Context, node, pool string) error {
	resp, err := c.client.ConvertPoolToThin(ctx, &sdspb.ConvertPoolToThinRequest{Node: node, Pool: pool})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// AddPoolCache puts a fast device in front of one node's LVM pool. mode may be
// empty, which the controller resolves to writethrough. It returns the mode
// actually applied and the size of the cache.
func (c *SDSClient) AddPoolCache(ctx context.Context, node, pool, device, mode string) (string, uint64, error) {
	resp, err := c.client.AddPoolCache(ctx, &sdspb.AddPoolCacheRequest{
		Node: node, Pool: pool, Device: device, Mode: mode,
	})
	if err != nil {
		return "", 0, err
	}
	if !resp.Success {
		return "", 0, fmt.Errorf("%s", resp.Message)
	}
	return resp.Mode, resp.CacheSizeBytes, nil
}

// RemovePoolCache flushes and detaches a pool's cache.
func (c *SDSClient) RemovePoolCache(ctx context.Context, node, pool string) error {
	resp, err := c.client.RemovePoolCache(ctx, &sdspb.RemovePoolCacheRequest{Node: node, Pool: pool})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}
