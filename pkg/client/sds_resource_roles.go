package client

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// SetPrimary sets a node as Primary for a resource
func (c *SDSClient) SetPrimary(ctx context.Context, resource, node string, force bool) error {
	req := &sdspb.SetPrimaryRequest{
		Resource: resource,
		Node:     node,
		Force:    force,
	}

	resp, err := c.client.SetPrimary(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// PromoteForNode requests a SAFE hard-failover promote of resource on node.
// The controller tries a normal promote first and only escalates to a forced
// promote if the node holds DRBD quorum; it refuses (returns an error) when the
// node lacks quorum, avoiding split-brain. This is what the CSI node plugin
// uses so a Pod rescheduled after a hard node failure can take over its RWO
// volume iff doing so is safe.
func (c *SDSClient) PromoteForNode(ctx context.Context, resource, node string) error {
	req := &sdspb.SetPrimaryRequest{
		Resource:      resource,
		Node:          node,
		QuorumGuarded: true,
	}

	resp, err := c.client.SetPrimary(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// SetSecondary sets a node as Secondary for a resource
func (c *SDSClient) SetSecondary(ctx context.Context, resource, node string) error {
	req := &sdspb.SetSecondaryRequest{
		Resource: resource,
		Node:     node,
	}

	resp, err := c.client.SetSecondary(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// SetDualPrimary toggles DRBD's allow-two-primaries for a resource, bracketing
// a hypervisor live migration (both hosts hold the disk open during hand-off).
//
// Enabling is refused for WAN resources; disabling is idempotent and verified,
// so it is safe to call unconditionally from a cleanup path.
func (c *SDSClient) SetDualPrimary(ctx context.Context, resource string, enable bool) error {
	resp, err := c.client.SetDualPrimary(ctx, &sdspb.SetDualPrimaryRequest{
		Resource: resource,
		Enable:   enable,
	})
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// CreateFilesystem creates a filesystem on a DRBD device
func (c *SDSClient) CreateFilesystem(ctx context.Context, resource string, volumeID uint32, node, fstype string) error {
	req := &sdspb.CreateFilesystemRequest{
		Resource: resource,
		VolumeId: volumeID,
		Fstype:   fstype,
		Node:     node,
	}

	resp, err := c.client.CreateFilesystem(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// MountResource mounts a DRBD device
func (c *SDSClient) MountResource(ctx context.Context, resource string, volumeID uint32, path, node, fstype string) error {
	req := &sdspb.MountResourceRequest{
		Resource: resource,
		VolumeId: volumeID,
		Path:     path,
		Node:     node,
		Fstype:   fstype,
	}

	resp, err := c.client.MountResource(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// UnmountResource unmounts a DRBD device
func (c *SDSClient) UnmountResource(ctx context.Context, resource string, volumeID uint32, node string) error {
	req := &sdspb.UnmountResourceRequest{
		Resource: resource,
		VolumeId: volumeID,
		Node:     node,
	}

	resp, err := c.client.UnmountResource(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}
