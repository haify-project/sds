package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// CreateResource creates a DRBD resource with LVM backend (default)
func (c *HaifyClient) CreateResource(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, drbdOptions map[string]string) error {
	return c.CreateResourceWithPool(ctx, name, port, nodes, protocol, sizeGB, "", drbdOptions)
}

// CreateResourceWithPool creates a DRBD resource with specified pool and LVM backend
func (c *HaifyClient) CreateResourceWithPool(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, drbdOptions map[string]string) error {
	return c.CreateResourceWithPoolAndType(ctx, name, port, nodes, protocol, sizeGB, pool, "lvm", drbdOptions)
}

// CreateResourceWithPoolAndType creates a DRBD resource with specified pool and storage type
func (c *HaifyClient) CreateResourceWithPoolAndType(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, storageType string, drbdOptions map[string]string) error {
	return c.CreateResourceRequest(ctx, &haifypb.CreateResourceRequest{
		Name:        name,
		Port:        port,
		Nodes:       nodes,
		Protocol:    protocol,
		SizeGb:      sizeGB,
		Pool:        pool,
		StorageType: storageType,
		DrbdOptions: drbdOptions,
	})
}

// CreateResourceRequest creates a resource from the full API request. It is the
// metadata-aware entry point used by CSI and MCP while legacy wrappers remain
// source-compatible.
func (c *HaifyClient) CreateResourceRequest(ctx context.Context, req *haifypb.CreateResourceRequest) error {
	resp, err := c.client.CreateResource(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// CreateResourceAutoPlace creates a DRBD resource without naming nodes: the
// controller auto-places `replicas` copies on the nodes with the most free
// space in the target pool. LAN-only (WAN needs an explicit primary).
func (c *HaifyClient) CreateResourceAutoPlace(ctx context.Context, name string, port uint32, replicas uint32, replicasOnDifferent, replicasOnSame, doNotPlaceWith []string, protocol string, sizeGB uint32, pool, storageType string, drbdOptions map[string]string) error {
	resp, err := c.client.CreateResource(ctx, &haifypb.CreateResourceRequest{
		Name:                name,
		Port:                port,
		Replicas:            replicas,
		ReplicasOnDifferent: replicasOnDifferent,
		ReplicasOnSame:      replicasOnSame,
		DoNotPlaceWith:      doNotPlaceWith,
		Protocol:            protocol,
		SizeGb:              sizeGB,
		Pool:                pool,
		StorageType:         storageType,
		DrbdOptions:         drbdOptions,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// CreateResourceWithVolumes creates a DRBD resource with one or more volumes
// specified explicitly. The volumes slice maps to CreateResourceRequest.Volumes
// (volume 0..N); size_gb/pool on the request are left zero and ignored by the
// controller when volumes is non-empty. storageType applies to every volume.
func (c *HaifyClient) CreateResourceWithVolumes(ctx context.Context, name string, port uint32, nodes []string, protocol, storageType string, drbdOptions map[string]string, volumes []*haifypb.VolumeSpec) error {
	req := &haifypb.CreateResourceRequest{
		Name:        name,
		Port:        port,
		Nodes:       nodes,
		Protocol:    protocol,
		StorageType: storageType,
		DrbdOptions: drbdOptions,
		Volumes:     volumes,
	}

	resp, err := c.client.CreateResource(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// CreateZFSResource creates a DRBD resource with ZFS backend
func (c *HaifyClient) CreateZFSResource(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, drbdOptions map[string]string) error {
	return c.CreateResourceWithPoolAndType(ctx, name, port, nodes, protocol, sizeGB, pool, "zfs", drbdOptions)
}

// CreateResourceWAN creates an opt-in WAN-replicated DRBD resource: it
// replicates between the single primary node and drNode across the internet via
// a per-resource haify-proxy pair. WAN forces protocol A; wanPort 0 lets the
// controller pick a random high port. Absent WAN flags, callers use the plain
// Create* methods above (which leave Wan=false ⇒ an unchanged LAN request).
func (c *HaifyClient) CreateResourceWAN(ctx context.Context, name string, port uint32, primaryNode string, sizeGB uint32, pool, storageType string, drbdOptions map[string]string, drNode, drEndpoint string, wanPort uint32) error {
	req := &haifypb.CreateResourceRequest{
		Name:        name,
		Port:        port,
		Nodes:       []string{primaryNode},
		Protocol:    "A",
		SizeGb:      sizeGB,
		Pool:        pool,
		StorageType: storageType,
		DrbdOptions: drbdOptions,
		Wan:         true,
		DrNode:      drNode,
		DrEndpoint:  drEndpoint,
		WanPort:     wanPort,
	}

	resp, err := c.client.CreateResource(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// AdoptResource imports an already-existing (foreign) DRBD resource into Haify
// management by recording its metadata. nodes/port/protocol may be left empty
// (nil/0/"") to auto-discover them from the live .res on a node. It never
// creates or modifies the DRBD resource or its data. Returns the metadata that
// was recorded.
func (c *HaifyClient) AdoptResource(ctx context.Context, name string, nodes []string, port uint32, protocol string) (*haifypb.AdoptResourceResponse, error) {
	req := &haifypb.AdoptResourceRequest{
		Name:     name,
		Nodes:    nodes,
		Port:     port,
		Protocol: protocol,
	}

	resp, err := c.client.AdoptResource(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp, nil
}

// GetResource gets resource information
func (c *HaifyClient) GetResource(ctx context.Context, name string) (*haifypb.ResourceInfo, error) {
	req := &haifypb.GetResourceRequest{
		Name: name,
	}

	resp, err := c.client.GetResource(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Resource, nil
}

// ListResources lists all resources
func (c *HaifyClient) ListResources(ctx context.Context) ([]*haifypb.ResourceInfo, error) {
	req := &haifypb.ListResourcesRequest{}

	resp, err := c.client.ListResources(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Resources, nil
}

// DeleteResource deletes a DRBD resource
func (c *HaifyClient) DeleteResource(ctx context.Context, name string) error {
	req := &haifypb.DeleteResourceRequest{
		Name: name,
	}

	resp, err := c.client.DeleteResource(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// AddVolume adds a volume to a resource
func (c *HaifyClient) AddVolume(ctx context.Context, resource, volume, pool string, sizeGB uint32) error {
	req := &haifypb.AddVolumeRequest{
		Resource: resource,
		Volume:   volume,
		Pool:     pool,
		SizeGb:   sizeGB,
	}

	resp, err := c.client.AddVolume(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// UpdateResourceOptions applies DRBD options ("section/key" -> value) to an
// existing resource and runs drbdadm adjust across the cluster.
// RepairResource reconciles every participant's copy of a resource's config.
func (c *HaifyClient) RepairResource(ctx context.Context, resource string) error {
	resp, err := c.client.RepairResource(ctx, &haifypb.RepairResourceRequest{Name: resource})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *HaifyClient) UpdateResourceOptions(ctx context.Context, resource string, options map[string]string) error {
	resp, err := c.client.UpdateResourceOptions(ctx, &haifypb.UpdateResourceOptionsRequest{
		Name:    resource,
		Options: options,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveVolume removes a volume from a resource
func (c *HaifyClient) RemoveVolume(ctx context.Context, resource string, volumeID uint32) error {
	req := &haifypb.RemoveVolumeRequest{
		Resource: resource,
		VolumeId: volumeID,
	}

	resp, err := c.client.RemoveVolume(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// ResizeVolume resizes a volume
func (c *HaifyClient) ResizeVolume(ctx context.Context, resource string, volumeID uint32, sizeGB uint32) error {
	return c.resizeVolume(ctx, resource, volumeID, sizeGB, false)
}

// ResizeVolumeIgnoringFreeSpace is ResizeVolume without the check that every
// replica's pool has room for the growth.
func (c *HaifyClient) ResizeVolumeIgnoringFreeSpace(ctx context.Context, resource string, volumeID uint32, sizeGB uint32) error {
	return c.resizeVolume(ctx, resource, volumeID, sizeGB, true)
}

func (c *HaifyClient) resizeVolume(ctx context.Context, resource string, volumeID uint32, sizeGB uint32, ignoreFreeSpace bool) error {
	req := &haifypb.ResizeVolumeRequest{
		Resource:        resource,
		VolumeId:        volumeID,
		SizeGb:          sizeGB,
		IgnoreFreeSpace: ignoreFreeSpace,
	}

	resp, err := c.client.ResizeVolume(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// ResourceStatus gets resource detailed status
func (c *HaifyClient) ResourceStatus(ctx context.Context, name string) (*haifypb.ResourceStatus, error) {
	req := &haifypb.ResourceStatusRequest{
		Name: name,
	}

	resp, err := c.client.ResourceStatus(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Status, nil
}

// DRFailback moves a WAN resource back from its DR node to the primary site,
// or advances a failback in progress; see DRFailbackRequest. The response's
// phase says whether to run it again.
// VerifyResource starts, follows or — with resync — repairs an online verify
// of a resource's replicas; see VerifyResourceRequest.
func (c *HaifyClient) VerifyResource(ctx context.Context, req *haifypb.VerifyResourceRequest) (*haifypb.VerifyResourceResponse, error) {
	return c.client.VerifyResource(ctx, req)
}
