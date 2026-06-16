// Package client provides SDS controller gRPC client
package client

import (
	"context"
	"fmt"

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
// Option customizes the SDS client connection.
type Option func(*clientOptions)

type clientOptions struct {
	token string
}

// WithToken attaches a static bearer token to every RPC, matching the
// controller's [auth] configuration.
func WithToken(token string) Option {
	return func(o *clientOptions) { o.token = token }
}

// tokenCredentials implements credentials.PerRPCCredentials for the static
// bearer-token scheme. The cluster API runs on a trusted management network
// without transport TLS, so transport security is not required.
type tokenCredentials struct {
	token string
}

func (t tokenCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + t.token}, nil
}

func (t tokenCredentials) RequireTransportSecurity() bool { return false }

func NewSDSClient(addr string, opts ...Option) (*SDSClient, error) {
	var options clientOptions
	for _, opt := range opts {
		opt(&options)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	if options.token != "" {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(tokenCredentials{token: options.token}))
	}

	conn, err := grpc.NewClient(addr, dialOpts...)
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
		return nil, fmt.Errorf("%s", resp.Message)
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
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Nodes, nil
}

// GetNode retrieves a node by address, hostname, or registered name.
func (c *SDSClient) GetNode(ctx context.Context, address string) (*sdspb.NodeInfo, error) {
	req := &sdspb.GetNodeRequest{
		Address: address,
	}

	resp, err := c.client.GetNode(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Node, nil
}

// UnregisterNode unregisters a node
func (c *SDSClient) UnregisterNode(ctx context.Context, address string) error {
	req := &sdspb.UnregisterNodeRequest{
		Address: address,
	}

	resp, err := c.client.UnregisterNode(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// HealthCheck performs a health check on a node
func (c *SDSClient) HealthCheck(ctx context.Context, node string) (*NodeHealthInfo, error) {
	req := &sdspb.HealthCheckRequest{
		Node: node,
	}

	resp, err := c.client.HealthCheck(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return &NodeHealthInfo{
		DrbdInstalled:           resp.Health.DrbdInstalled,
		DrbdVersion:             resp.Health.DrbdVersion,
		DrbdReactorInstalled:    resp.Health.DrbdReactorInstalled,
		DrbdReactorVersion:      resp.Health.DrbdReactorVersion,
		DrbdReactorRunning:      resp.Health.DrbdReactorRunning,
		ResourceAgentsInstalled: resp.Health.ResourceAgentsInstalled,
		AvailableAgents:         resp.Health.AvailableAgents,
	}, nil
}

// NodeHealthInfo represents the health status of a node
type NodeHealthInfo struct {
	DrbdInstalled           bool     `json:"drbd_installed"`
	DrbdVersion             string   `json:"drbd_version"`
	DrbdReactorInstalled    bool     `json:"drbd_reactor_installed"`
	DrbdReactorVersion      string   `json:"drbd_reactor_version"`
	DrbdReactorRunning      bool     `json:"drbd_reactor_running"`
	ResourceAgentsInstalled bool     `json:"resource_agents_installed"`
	AvailableAgents         []string `json:"available_agents"`
}

// ==================== RESOURCE OPERATIONS ====================

// CreateResource creates a DRBD resource with LVM backend (default)
func (c *SDSClient) CreateResource(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, drbdOptions map[string]string) error {
	return c.CreateResourceWithPool(ctx, name, port, nodes, protocol, sizeGB, "", drbdOptions)
}

// CreateResourceWithPool creates a DRBD resource with specified pool and LVM backend
func (c *SDSClient) CreateResourceWithPool(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, drbdOptions map[string]string) error {
	return c.CreateResourceWithPoolAndType(ctx, name, port, nodes, protocol, sizeGB, pool, "lvm", drbdOptions)
}

// CreateResourceWithPoolAndType creates a DRBD resource with specified pool and storage type
func (c *SDSClient) CreateResourceWithPoolAndType(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, storageType string, drbdOptions map[string]string) error {
	req := &sdspb.CreateResourceRequest{
		Name:        name,
		Port:        port,
		Nodes:       nodes,
		Protocol:    protocol,
		SizeGb:      sizeGB,
		Pool:        pool,
		StorageType: storageType,
		DrbdOptions: drbdOptions,
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
func (c *SDSClient) CreateZFSResource(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, drbdOptions map[string]string) error {
	return c.CreateResourceWithPoolAndType(ctx, name, port, nodes, protocol, sizeGB, pool, "zfs", drbdOptions)
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
		return nil, fmt.Errorf("%s", resp.Message)
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
		return nil, fmt.Errorf("%s", resp.Message)
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
		return fmt.Errorf("%s", resp.Message)
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
		return fmt.Errorf("%s", resp.Message)
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
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// UpdateResourceOptions applies DRBD options ("section/key" -> value) to an
// existing resource and runs drbdadm adjust across the cluster.
func (c *SDSClient) UpdateResourceOptions(ctx context.Context, resource string, options map[string]string) error {
	resp, err := c.client.UpdateResourceOptions(ctx, &sdspb.UpdateResourceOptionsRequest{
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
func (c *SDSClient) RemoveVolume(ctx context.Context, resource string, volumeID uint32) error {
	req := &sdspb.RemoveVolumeRequest{
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
func (c *SDSClient) ResizeVolume(ctx context.Context, resource string, volumeID uint32, sizeGB uint32) error {
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
		return fmt.Errorf("%s", resp.Message)
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
		return nil, fmt.Errorf("%s", resp.Message)
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
		return "", fmt.Errorf("%s", resp.Message)
	}

	return resp.ConfigPath, nil
}

// EnableSelfHa makes the controller itself highly available. It returns the
// metadata resource name and the node-local handoff log path.
func (c *SDSClient) EnableSelfHa(ctx context.Context, vip, pool string, sizeGB, port uint32, nodes []string) (string, string, error) {
	resp, err := c.client.EnableSelfHa(ctx, &sdspb.EnableSelfHaRequest{
		Vip:    vip,
		Pool:   pool,
		SizeGb: sizeGB,
		Port:   port,
		Nodes:  nodes,
	})
	if err != nil {
		return "", "", err
	}
	if !resp.Success {
		return "", "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Resource, resp.HandoffLog, nil
}

// DisableSelfHa reverts the controller to standalone operation on node.
func (c *SDSClient) DisableSelfHa(ctx context.Context, node string) error {
	resp, err := c.client.DisableSelfHa(ctx, &sdspb.DisableSelfHaRequest{Node: node})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// SelfHaStatus describes the controller self-HA state.
type SelfHaStatus struct {
	Enabled    bool
	Resource   string
	VIP        string
	Nodes      []string
	ActiveNode string
}

// GetSelfHaStatus reports the controller self-HA state.
func (c *SDSClient) GetSelfHaStatus(ctx context.Context) (*SelfHaStatus, error) {
	resp, err := c.client.GetSelfHaStatus(ctx, &sdspb.GetSelfHaStatusRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return &SelfHaStatus{
		Enabled:    resp.Enabled,
		Resource:   resp.Resource,
		VIP:        resp.Vip,
		Nodes:      resp.Nodes,
		ActiveNode: resp.ActiveNode,
	}, nil
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
		return fmt.Errorf("%s", resp.Message)
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
		return fmt.Errorf("%s", resp.Message)
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
		return nil, fmt.Errorf("%s", resp.Message)
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
		return nil, fmt.Errorf("%s", resp.Message)
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

// CreateSnapshotSchedule creates a cron-driven snapshot schedule with GFS retention.
func (c *SDSClient) CreateSnapshotSchedule(ctx context.Context, resource, cron string, keep *sdspb.GFSRetention, enabled bool) error {
	resp, err := c.client.CreateSnapshotSchedule(ctx, &sdspb.CreateSnapshotScheduleRequest{
		Resource: resource,
		Cron:     cron,
		Keep:     keep,
		Enabled:  enabled,
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

// ==================== GATEWAY OPERATIONS ====================

// CreateNFSGateway creates an NFS gateway
func (c *SDSClient) CreateNFSGateway(ctx context.Context, req *sdspb.CreateNFSGatewayRequest) (*sdspb.CreateNFSGatewayResponse, error) {
	resp, err := c.client.CreateNFSGateway(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return resp, fmt.Errorf("%s", resp.Message)
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
		return resp, fmt.Errorf("%s", resp.Message)
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
		return resp, fmt.Errorf("%s", resp.Message)
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
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Gateways, nil
}

// GetGateway gets a gateway by resource identifier.
func (c *SDSClient) GetGateway(ctx context.Context, id string) (*sdspb.GatewayInfo, error) {
	req := &sdspb.GetGatewayRequest{Id: id}

	resp, err := c.client.GetGateway(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	return resp.Gateway, nil
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
		return fmt.Errorf("%s", resp.Message)
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
		return fmt.Errorf("%s", resp.Message)
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
		return fmt.Errorf("%s", resp.Message)
	}

	return nil
}

// AddNFSExport adds an export to an NFS gateway.
func (c *SDSClient) AddNFSExport(ctx context.Context, resource, exportPath string, fsid int32, clientSpec, options string) error {
	req := &sdspb.AddNFSExportRequest{
		Resource:   resource,
		ExportPath: exportPath,
		Fsid:       fsid,
		ClientSpec: clientSpec,
		Options:    options,
	}

	resp, err := c.client.AddNFSExport(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveNFSExport removes an export from an NFS gateway.
func (c *SDSClient) RemoveNFSExport(ctx context.Context, resource, exportPath string) error {
	req := &sdspb.RemoveNFSExportRequest{
		Resource:   resource,
		ExportPath: exportPath,
	}

	resp, err := c.client.RemoveNFSExport(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListNFSExports lists exports for an NFS gateway.
func (c *SDSClient) ListNFSExports(ctx context.Context, resource string) ([]*sdspb.NFSExportInfo, error) {
	req := &sdspb.ListNFSExportsRequest{Resource: resource}

	resp, err := c.client.ListNFSExports(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Exports, nil
}

// AddISCSILUN adds a LUN to an iSCSI gateway.
func (c *SDSClient) AddISCSILUN(ctx context.Context, resource string, lun int32, device string) error {
	req := &sdspb.AddISCSILUNRequest{Resource: resource, Lun: lun, Device: device}

	resp, err := c.client.AddISCSILUN(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveISCSILUN removes a LUN from an iSCSI gateway.
func (c *SDSClient) RemoveISCSILUN(ctx context.Context, resource string, lun int32) error {
	req := &sdspb.RemoveISCSILUNRequest{Resource: resource, Lun: lun}

	resp, err := c.client.RemoveISCSILUN(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListISCSILUNs lists LUNs for an iSCSI gateway.
func (c *SDSClient) ListISCSILUNs(ctx context.Context, resource string) ([]*sdspb.ISCSILUNInfo, error) {
	req := &sdspb.ListISCSILUNsRequest{Resource: resource}

	resp, err := c.client.ListISCSILUNs(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Luns, nil
}

// AddISCSIInitiator adds an initiator ACL to an iSCSI gateway.
func (c *SDSClient) AddISCSIInitiator(ctx context.Context, resource, initiator string) error {
	req := &sdspb.AddISCSIInitiatorRequest{Resource: resource, Initiator: initiator}

	resp, err := c.client.AddISCSIInitiator(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveISCSIInitiator removes an initiator ACL from an iSCSI gateway.
func (c *SDSClient) RemoveISCSIInitiator(ctx context.Context, resource, initiator string) error {
	req := &sdspb.RemoveISCSIInitiatorRequest{Resource: resource, Initiator: initiator}

	resp, err := c.client.RemoveISCSIInitiator(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListISCSIInitiators lists initiator ACLs for an iSCSI gateway.
func (c *SDSClient) ListISCSIInitiators(ctx context.Context, resource string) ([]string, error) {
	req := &sdspb.ListISCSIInitiatorsRequest{Resource: resource}

	resp, err := c.client.ListISCSIInitiators(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Initiators, nil
}

// SetISCSIChap updates CHAP settings on an iSCSI gateway.
func (c *SDSClient) SetISCSIChap(ctx context.Context, resource, username, password string, mutual bool) error {
	req := &sdspb.SetISCSIChapRequest{
		Resource: resource,
		Username: username,
		Password: password,
		Mutual:   mutual,
	}

	resp, err := c.client.SetISCSIChap(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// GetISCSIChap returns CHAP settings for an iSCSI gateway.
func (c *SDSClient) GetISCSIChap(ctx context.Context, resource string) (*sdspb.GetISCSIChapResponse, error) {
	req := &sdspb.GetISCSIChapRequest{Resource: resource}

	resp, err := c.client.GetISCSIChap(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// AddNVMeNamespace adds a namespace to an NVMe gateway.
func (c *SDSClient) AddNVMeNamespace(ctx context.Context, resource, device string) error {
	req := &sdspb.AddNVMeNamespaceRequest{Resource: resource, Device: device}

	resp, err := c.client.AddNVMeNamespace(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveNVMeNamespace removes a namespace from an NVMe gateway.
func (c *SDSClient) RemoveNVMeNamespace(ctx context.Context, resource string, namespaceID int32) error {
	req := &sdspb.RemoveNVMeNamespaceRequest{Resource: resource, NamespaceId: namespaceID}

	resp, err := c.client.RemoveNVMeNamespace(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListNVMeNamespaces lists namespaces for an NVMe gateway.
func (c *SDSClient) ListNVMeNamespaces(ctx context.Context, resource string) ([]*sdspb.NVMeNamespaceInfo, error) {
	req := &sdspb.ListNVMeNamespacesRequest{Resource: resource}

	resp, err := c.client.ListNVMeNamespaces(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Namespaces, nil
}

// AddNVMeHost adds a host allow-list entry to an NVMe gateway.
func (c *SDSClient) AddNVMeHost(ctx context.Context, resource, hostNQN string) error {
	req := &sdspb.AddNVMeHostRequest{Resource: resource, HostNqn: hostNQN}

	resp, err := c.client.AddNVMeHost(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// RemoveNVMeHost removes a host allow-list entry from an NVMe gateway.
func (c *SDSClient) RemoveNVMeHost(ctx context.Context, resource, hostNQN string) error {
	req := &sdspb.RemoveNVMeHostRequest{Resource: resource, HostNqn: hostNQN}

	resp, err := c.client.RemoveNVMeHost(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// ListNVMeHosts lists hosts configured for an NVMe gateway.
func (c *SDSClient) ListNVMeHosts(ctx context.Context, resource string) ([]string, error) {
	req := &sdspb.ListNVMeHostsRequest{Resource: resource}

	resp, err := c.client.ListNVMeHosts(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Hosts, nil
}

// ==================== ZFS POOL OPERATIONS ====================

// CreateZFSPool creates a ZFS pool
func (c *SDSClient) CreateZFSPool(ctx context.Context, name, node string, vdevs []string, thin bool) error {
	req := &sdspb.CreateZFSPoolRequest{
		Name:  name,
		Node:  node,
		Vdevs: vdevs,
		Thin:  thin,
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

// ==================== ZFS SNAPSHOT OPERATIONS ====================

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

// ==================== LVM SNAPSHOT OPERATIONS ====================

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

// ListLvmSnapshots lists LVM snapshots for a pool (VG)
func (c *SDSClient) ListLvmSnapshots(ctx context.Context, pool, node string) ([]*sdspb.SnapshotInfo, error) {
	req := &sdspb.ListLvmSnapshotsRequest{
		LvName: pool, // Mapped to VG Name
		Node:   node,
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
