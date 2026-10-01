package deployment

import (
	"context"
	"fmt"
	"strings"
)

// ============ ZFS Operations ============

// ZFSCreatePool creates a ZFS pool
func (c *Client) ZFSCreatePool(ctx context.Context, hosts []string, poolName string, vdevs []string, opts ...ZFSOption) (*ExecResult, error) {
	// A zpool has no thin/thick mode; it is just the aggregation of vdevs.
	// Thin vs thick provisioning is a per-zvol property decided at volume
	// creation time (zfs create -s -V / refreservation), not at the pool level,
	// so there are currently no pool-level options to apply here.
	_ = opts
	cmd := fmt.Sprintf("sudo zpool create -f %s %s", poolName, strings.Join(vdevs, " "))
	return c.Exec(ctx, hosts, cmd)
}

// ZFSDestroyPool destroys a ZFS pool
func (c *Client) ZFSDestroyPool(ctx context.Context, hosts []string, poolName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zpool destroy -f %s", poolName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSListPools lists ZFS pools
func (c *Client) ZFSListPools(ctx context.Context, hosts []string) (*ExecResult, error) {
	cmd := "sudo zpool list -Hp -o name,size,free,alloc,cap"
	return c.Exec(ctx, hosts, cmd)
}

// ZFSGetPool gets ZFS pool status
func (c *Client) ZFSGetPool(ctx context.Context, hosts []string, poolName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zpool status %s", poolName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSCreateDataset creates a ZFS dataset
func (c *Client) ZFSCreateDataset(ctx context.Context, hosts []string, datasetName string, opts ...ZFSOption) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs create %s", datasetName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSCreateThinDataset creates a thin-provisioned ZFS dataset (zvol)
func (c *Client) ZFSCreateThinDataset(ctx context.Context, hosts []string, poolName, datasetName, size string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs create -s -V %s %s/%s", size, poolName, datasetName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSDestroyDataset destroys a ZFS dataset
func (c *Client) ZFSDestroyDataset(ctx context.Context, hosts []string, datasetName string) (*ExecResult, error) {
	// -r removes dependent snapshots too: a dataset delete through the
	// management API is an explicit teardown, and without -r any dataset
	// that was ever snapshotted becomes undeletable ("has children").
	cmd := fmt.Sprintf("sudo zfs destroy -r -f %s", datasetName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSSnapshot creates a ZFS snapshot
func (c *Client) ZFSSnapshot(ctx context.Context, hosts []string, dataset, snapshotName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs snapshot %s@%s", dataset, snapshotName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSRollback rolls back to a ZFS snapshot
func (c *Client) ZFSRollback(ctx context.Context, hosts []string, dataset, snapshotName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs rollback -r %s@%s", dataset, snapshotName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSClone creates a clone from a snapshot
func (c *Client) ZFSClone(ctx context.Context, hosts []string, snapshot, clonePath string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs clone %s %s", snapshot, clonePath)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSListSnapshots lists ZFS snapshots for a dataset
func (c *Client) ZFSListSnapshots(ctx context.Context, hosts []string, dataset string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs list -t snapshot -o name,used,refer,creation -H %s", dataset)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSDestroySnapshot destroys a ZFS snapshot
func (c *Client) ZFSDestroySnapshot(ctx context.Context, hosts []string, snapshot string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs destroy -r %s", snapshot)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSSetQuota sets a quota on a ZFS dataset
func (c *Client) ZFSSetQuota(ctx context.Context, hosts []string, dataset, quota string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs set quota=%s %s", quota, dataset)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSSetReservation sets a reservation on a ZFS dataset
func (c *Client) ZFSSetReservation(ctx context.Context, hosts []string, dataset, reservation string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs set reservation=%s %s", reservation, dataset)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSResizeVolume resizes a ZFS volume
func (c *Client) ZFSResizeVolume(ctx context.Context, hosts []string, volumePath, newSize string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs set volsize=%s %s", newSize, volumePath)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSOption configures ZFS operations
type ZFSOption func(*zfsOptions)

type zfsOptions struct {
	compression bool
	dedup       bool
}

// WithZFSCompression enables compression for ZFS
func WithZFSCompression(compression bool) ZFSOption {
	return func(o *zfsOptions) {
		o.compression = compression
	}
}

// WithZFSDedup enables dedup for ZFS
func WithZFSDedup(dedup bool) ZFSOption {
	return func(o *zfsOptions) {
		o.dedup = dedup
	}
}
