// Package client provides SDS controller gRPC client
package client

import (
	"context"
	"fmt"
	"time"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// SDSClient wraps SDS controller gRPC client
type SDSClient struct {
	conn   *grpc.ClientConn
	client sdspb.SDSControllerClient
	addr   string
}

// NewSDSClient creates a new SDS controller client
func NewSDSClient(addr string) (*SDSClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SDS controller at %s: %w", addr, err)
	}

	return &SDSClient{
		conn:   conn,
		client: sdspb.NewSDSControllerClient(conn),
		addr:   addr,
	}, nil
}

// Close closes the connection
func (c *SDSClient) Close() error {
	return c.conn.Close()
}

// Address returns the controller address
func (c *SDSClient) Address() string {
	return c.addr
}

// ==================== POOL OPERATIONS ====================

// CreatePool creates a storage pool
func (c *SDSClient) CreatePool(ctx context.Context, name, poolType, node string, disks []string, sizeGB uint64) error {
	req := &sdspb.CreatePoolRequest{
		Name:    name,
		Type:    poolType,
		Node:    node,
		Disks:   disks,
		SizeGb:  sizeGB,
	}

	resp, err := c.client.CreatePool(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
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
		return nil, fmt.Errorf(resp.Message)
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
		return nil, fmt.Errorf(resp.Message)
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
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// ==================== NODE OPERATIONS ====================

// RegisterNode registers a new node
func (c *SDSClient) RegisterNode(ctx context.Context, name, address string) (*sdspb.NodeInfo, error) {
	req := &sdspb.RegisterNodeRequest{
		Name:    name,
		Address: address,
	}

	resp, err := c.client.RegisterNode(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf(resp.Message)
	}

	return resp.Node, nil
}

// ListNodes lists all nodes
func (c *SDSClient) ListNodes(ctx context.Context) ([]*sdspb.NodeInfo, error) {
	req := &sdspb.ListNodesRequest{}

	resp, err := c.client.ListNodes(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf(resp.Message)
	}

	return resp.Nodes, nil
}

// ==================== RESOURCE OPERATIONS ====================

// CreateResource creates a DRBD resource
func (c *SDSClient) CreateResource(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32) error {
	return c.CreateResourceWithPool(ctx, name, port, nodes, protocol, sizeGB, "")
}

// CreateResourceWithPool creates a DRBD resource with specified pool
func (c *SDSClient) CreateResourceWithPool(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string) error {
	req := &sdspb.CreateResourceRequest{
		Name:     name,
		Port:     port,
		Nodes:    nodes,
		Protocol: protocol,
		SizeGb:   sizeGB,
		Pool:     pool,
	}

	resp, err := c.client.CreateResource(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// GetResource gets resource information
func (c *SDSClient) GetResource(ctx context.Context, name string) (*sdspb.ResourceInfo, error) {
	req := &sdspb.GetResourceRequest{
		Name: name,
	}

	resp, err := c.client.GetResource(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf(resp.Message)
	}

	return resp.Resource, nil
}

// ListResources lists all resources
func (c *SDSClient) ListResources(ctx context.Context) ([]*sdspb.ResourceInfo, error) {
	req := &sdspb.ListResourcesRequest{}

	resp, err := c.client.ListResources(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf(resp.Message)
	}

	return resp.Resources, nil
}

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
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// DeleteResource deletes a DRBD resource
func (c *SDSClient) DeleteResource(ctx context.Context, name string) error {
	req := &sdspb.DeleteResourceRequest{
		Name: name,
	}

	resp, err := c.client.DeleteResource(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// AddVolume adds a volume to a resource
func (c *SDSClient) AddVolume(ctx context.Context, resource, volume, pool string, sizeGB uint32) error {
	req := &sdspb.AddVolumeRequest{
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
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// RemoveVolume removes a volume from a resource
func (c *SDSClient) RemoveVolume(ctx context.Context, resource string, volumeID uint32, node string) error {
	req := &sdspb.RemoveVolumeRequest{
		Resource: resource,
		VolumeId: volumeID,
	}

	resp, err := c.client.RemoveVolume(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// ResizeVolume resizes a volume
func (c *SDSClient) ResizeVolume(ctx context.Context, resource string, volumeID uint32, node string, sizeGB uint32) error {
	req := &sdspb.ResizeVolumeRequest{
		Resource: resource,
		VolumeId: volumeID,
		SizeGb:   sizeGB,
	}

	resp, err := c.client.ResizeVolume(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// ResourceStatus gets resource detailed status
func (c *SDSClient) ResourceStatus(ctx context.Context, name string) (*sdspb.ResourceStatus, error) {
	req := &sdspb.ResourceStatusRequest{
		Name: name,
	}

	resp, err := c.client.ResourceStatus(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf(resp.Message)
	}

	return resp.Status, nil
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
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// CreateFilesystem creates a filesystem on a DRBD device
func (c *SDSClient) CreateFilesystem(ctx context.Context, resource string, volumeID uint32, node, fstype string) error {
	req := &sdspb.CreateFilesystemRequest{
		Resource: resource,
		VolumeId: volumeID,
		Fstype:   fstype,
	}

	resp, err := c.client.CreateFilesystem(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
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
		return fmt.Errorf(resp.Message)
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
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// MakeHa creates a drbd-reactor promoter config for HA failover
func (c *SDSClient) MakeHa(ctx context.Context, resource string, services []string, mountPoint, fsType, vip string) (string, error) {
	req := &sdspb.MakeHaRequest{
		Resource:   resource,
		Services:   services,
		MountPoint: mountPoint,
		Fstype:     fsType,
		Vip:        vip,
	}

	resp, err := c.client.MakeHa(ctx, req)
	if err != nil {
		return "", err
	}

	if !resp.Success {
		return "", fmt.Errorf(resp.Message)
	}

	return resp.ConfigPath, nil
}

// EvictHa evicts an HA resource from the active node
func (c *SDSClient) EvictHa(ctx context.Context, resource string) error {
	req := &sdspb.EvictHaRequest{
		Resource: resource,
	}

	resp, err := c.client.EvictHa(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// DeleteHa deletes an HA configuration
func (c *SDSClient) DeleteHa(ctx context.Context, resource string) error {
	req := &sdspb.DeleteHaRequest{
		Resource: resource,
	}

	resp, err := c.client.DeleteHa(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// GetHa gets an HA configuration
func (c *SDSClient) GetHa(ctx context.Context, resource string) (*sdspb.HaConfigInfo, error) {
	req := &sdspb.GetHaRequest{
		Resource: resource,
	}

	resp, err := c.client.GetHa(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf(resp.Message)
	}

	return resp.Config, nil
}

// ListHa lists all HA configurations
func (c *SDSClient) ListHa(ctx context.Context) ([]*sdspb.HaConfigInfo, error) {
	req := &sdspb.ListHaRequest{}

	resp, err := c.client.ListHa(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf(resp.Message)
	}

	return resp.Configs, nil
}

// ==================== SNAPSHOT OPERATIONS ====================

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
		return fmt.Errorf(resp.Message)
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
		return fmt.Errorf(resp.Message)
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
		return nil, fmt.Errorf(resp.Message)
	}

	return resp.Snapshots, nil
}

// ==================== GATEWAY OPERATIONS ====================

// CreateNFSGateway creates an NFS gateway
func (c *SDSClient) CreateNFSGateway(ctx context.Context, req *sdspb.CreateNFSGatewayRequest) (*sdspb.CreateNFSGatewayResponse, error) {
	resp, err := c.client.CreateNFSGateway(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return resp, fmt.Errorf(resp.Message)
	}

	return resp, nil
}

// CreateISCSIGateway creates an iSCSI gateway
func (c *SDSClient) CreateISCSIGateway(ctx context.Context, req *sdspb.CreateISCSIGatewayRequest) (*sdspb.CreateISCSIGatewayResponse, error) {
	resp, err := c.client.CreateISCSIGateway(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return resp, fmt.Errorf(resp.Message)
	}

	return resp, nil
}

// CreateNVMeGateway creates an NVMe gateway
func (c *SDSClient) CreateNVMeGateway(ctx context.Context, req *sdspb.CreateNVMeGatewayRequest) (*sdspb.CreateNVMeGatewayResponse, error) {
	resp, err := c.client.CreateNVMeGateway(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return resp, fmt.Errorf(resp.Message)
	}

	return resp, nil
}

// ListGateways lists all gateways
func (c *SDSClient) ListGateways(ctx context.Context) ([]*sdspb.GatewayInfo, error) {
	req := &sdspb.ListGatewaysRequest{}

	resp, err := c.client.ListGateways(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf(resp.Message)
	}

	return resp.Gateways, nil
}

// StartGateway starts a gateway
func (c *SDSClient) StartGateway(ctx context.Context, id string) error {
	req := &sdspb.StartGatewayRequest{
		Id: id,
	}

	resp, err := c.client.StartGateway(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// StopGateway stops a gateway
func (c *SDSClient) StopGateway(ctx context.Context, id string) error {
	req := &sdspb.StopGatewayRequest{
		Id: id,
	}

	resp, err := c.client.StopGateway(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
	}

	return nil
}

// DeleteGateway deletes a gateway
func (c *SDSClient) DeleteGateway(ctx context.Context, id string) error {
	req := &sdspb.DeleteGatewayRequest{
		Id: id,
	}

	resp, err := c.client.DeleteGateway(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf(resp.Message)
	}

	return nil
}
