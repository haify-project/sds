// Package client provides gRPC client for drbd-agent
package client

import (
	"context"
	"fmt"
	"time"

	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// AgentClient wraps drbd-agent gRPC clients for all services
type AgentClient struct {
	conn         *grpc.ClientConn
	drbdAgent    pb.DRBDAgentClient
	drbdCore     pb.DRBDCoreClient
	lvm          pb.LVMClient
	systemd      pb.SystemdClient
	drbdReactor  pb.DRBDReactorClient
	addr         string
}

// NewAgentClient creates a new drbd-agent client with all service clients
func NewAgentClient(addr string) (*AgentClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to drbd-agent at %s: %w", addr, err)
	}

	return &AgentClient{
		conn:         conn,
		drbdAgent:    pb.NewDRBDAgentClient(conn),
		drbdCore:     pb.NewDRBDCoreClient(conn),
		lvm:          pb.NewLVMClient(conn),
		systemd:      pb.NewSystemdClient(conn),
		drbdReactor:  pb.NewDRBDReactorClient(conn),
		addr:         addr,
	}, nil
}

// Close closes the connection
func (c *AgentClient) Close() error {
	return c.conn.Close()
}

// Address returns the agent address
func (c *AgentClient) Address() string {
	return c.addr
}

// ==================== DRBDAgent Service ====================

// HealthCheck performs a health check
func (c *AgentClient) HealthCheck(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	return c.drbdAgent.HealthCheck(ctx, req)
}

// SubscribeEvents subscribes to DRBD events
func (c *AgentClient) SubscribeEvents(ctx context.Context, req *pb.SubscribeEventsRequest) (pb.DRBDAgent_SubscribeEventsClient, error) {
	return c.drbdAgent.SubscribeEvents(ctx, req)
}

// WatchResources watches resource state changes
func (c *AgentClient) WatchResources(ctx context.Context, req *pb.WatchResourcesRequest) (pb.DRBDAgent_WatchResourcesClient, error) {
	return c.drbdAgent.WatchResources(ctx, req)
}

// ExecCommand executes a command on the node
func (c *AgentClient) ExecCommand(ctx context.Context, req *pb.ExecCommandRequest) (*pb.ExecCommandResponse, error) {
	return c.drbdAgent.ExecCommand(ctx, req)
}

// ==================== DRBDCore Service ====================

// Status gets DRBD resource status
func (c *AgentClient) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
	return c.drbdCore.Status(ctx, req)
}

// Up brings DRBD resources up
func (c *AgentClient) Up(ctx context.Context, req *pb.UpRequest) (*pb.UpResponse, error) {
	return c.drbdCore.Up(ctx, req)
}

// Down takes DRBD resources down
func (c *AgentClient) Down(ctx context.Context, req *pb.DownRequest) (*pb.DownResponse, error) {
	return c.drbdCore.Down(ctx, req)
}

// Primary promotes resources to Primary
func (c *AgentClient) Primary(ctx context.Context, req *pb.PrimaryRequest) (*pb.PrimaryResponse, error) {
	return c.drbdCore.Primary(ctx, req)
}

// Secondary demotes resources to Secondary
func (c *AgentClient) Secondary(ctx context.Context, req *pb.SecondaryRequest) (*pb.SecondaryResponse, error) {
	return c.drbdCore.Secondary(ctx, req)
}

// CreateMD creates DRBD metadata
func (c *AgentClient) CreateMD(ctx context.Context, req *pb.CreateMDRequest) (*pb.CreateMDResponse, error) {
	return c.drbdCore.CreateMD(ctx, req)
}

// Adjust adjusts DRBD resources
func (c *AgentClient) Adjust(ctx context.Context, req *pb.AdjustRequest) (*pb.AdjustResponse, error) {
	return c.drbdCore.Adjust(ctx, req)
}

// Resize resizes DRBD resources
func (c *AgentClient) Resize(ctx context.Context, req *pb.ResizeRequest) (*pb.ResizeResponse, error) {
	return c.drbdCore.Resize(ctx, req)
}

// AddVolume adds a volume to DRBD resource
func (c *AgentClient) AddVolume(ctx context.Context, req *pb.AddVolumeRequest) (*pb.AddVolumeResponse, error) {
	return c.drbdCore.AddVolume(ctx, req)
}

// GetResourceVolumes gets volume information for a DRBD resource
func (c *AgentClient) GetResourceVolumes(ctx context.Context, req *pb.GetResourceVolumesRequest) (*pb.GetResourceVolumesResponse, error) {
	return c.drbdCore.GetResourceVolumes(ctx, req)
}

// ==================== LVM Service ====================

// LVCreate creates an LVM logical volume
func (c *AgentClient) LVCreate(ctx context.Context, req *pb.LVCreateRequest) (*pb.LVCreateResponse, error) {
	return c.lvm.LVCreate(ctx, req)
}

// LVRemove removes an LVM logical volume
func (c *AgentClient) LVRemove(ctx context.Context, req *pb.LVRemoveRequest) (*pb.LVRemoveResponse, error) {
	return c.lvm.LVRemove(ctx, req)
}

// LVS lists logical volumes
func (c *AgentClient) LVS(ctx context.Context, req *pb.LVSRequest) (*pb.LVSResponse, error) {
	return c.lvm.LVS(ctx, req)
}

// LVSnapshotCreate creates an LVM snapshot
func (c *AgentClient) LVSnapshotCreate(ctx context.Context, req *pb.LVSnapshotCreateRequest) (*pb.LVSnapshotCreateResponse, error) {
	return c.lvm.LVSnapshotCreate(ctx, req)
}

// LVSnapshotRestore restores an LVM from snapshot
func (c *AgentClient) LVSnapshotRestore(ctx context.Context, req *pb.LVSnapshotRestoreRequest) (*pb.LVSnapshotRestoreResponse, error) {
	return c.lvm.LVSnapshotRestore(ctx, req)
}

// LVListSnapshots lists snapshots for an LV
func (c *AgentClient) LVListSnapshots(ctx context.Context, req *pb.LVListSnapshotsRequest) (*pb.LVListSnapshotsResponse, error) {
	return c.lvm.LVListSnapshots(ctx, req)
}

// VGCreate creates an LVM volume group
func (c *AgentClient) VGCreate(ctx context.Context, req *pb.VGCreateRequest) (*pb.VGCreateResponse, error) {
	return c.lvm.VGCreate(ctx, req)
}

// VGRemove removes an LVM volume group
func (c *AgentClient) VGRemove(ctx context.Context, req *pb.VGRemoveRequest) (*pb.VGRemoveResponse, error) {
	return c.lvm.VGRemove(ctx, req)
}

// VGS lists volume groups
func (c *AgentClient) VGS(ctx context.Context, req *pb.VGSRequest) (*pb.VGSResponse, error) {
	return c.lvm.VGS(ctx, req)
}

// VGExtend extends a volume group
func (c *AgentClient) VGExtend(ctx context.Context, req *pb.VGExtendRequest) (*pb.VGExtendResponse, error) {
	return c.lvm.VGExtend(ctx, req)
}

// LVExtend extends a logical volume
func (c *AgentClient) LVExtend(ctx context.Context, req *pb.LVExtendRequest) (*pb.LVExtendResponse, error) {
	return c.lvm.LVExtend(ctx, req)
}

// ==================== Systemd Service ====================

// SystemdServiceStart starts a systemd service
func (c *AgentClient) SystemdServiceStart(ctx context.Context, req *pb.SystemdServiceStartRequest) (*pb.SystemdServiceStartResponse, error) {
	return c.systemd.SystemdServiceStart(ctx, req)
}

// SystemdServiceStop stops a systemd service
func (c *AgentClient) SystemdServiceStop(ctx context.Context, req *pb.SystemdServiceStopRequest) (*pb.SystemdServiceStopResponse, error) {
	return c.systemd.SystemdServiceStop(ctx, req)
}

// SystemdServiceStatus gets systemd service status
func (c *AgentClient) SystemdServiceStatus(ctx context.Context, req *pb.SystemdServiceStatusRequest) (*pb.SystemdServiceStatusResponse, error) {
	return c.systemd.SystemdServiceStatus(ctx, req)
}

// SystemdServiceEnable enables a systemd service
func (c *AgentClient) SystemdServiceEnable(ctx context.Context, req *pb.SystemdServiceEnableRequest) (*pb.SystemdServiceEnableResponse, error) {
	return c.systemd.SystemdServiceEnable(ctx, req)
}

// SystemdServiceDisable disables a systemd service
func (c *AgentClient) SystemdServiceDisable(ctx context.Context, req *pb.SystemdServiceDisableRequest) (*pb.SystemdServiceDisableResponse, error) {
	return c.systemd.SystemdServiceDisable(ctx, req)
}

// SystemdCreateService creates a systemd service file
func (c *AgentClient) SystemdCreateService(ctx context.Context, req *pb.SystemdCreateServiceRequest) (*pb.SystemdCreateServiceResponse, error) {
	return c.systemd.SystemdCreateService(ctx, req)
}

// SystemdDeleteService deletes a systemd service file
func (c *AgentClient) SystemdDeleteService(ctx context.Context, req *pb.SystemdDeleteServiceRequest) (*pb.SystemdDeleteServiceResponse, error) {
	return c.systemd.SystemdDeleteService(ctx, req)
}

// ==================== DRBD-Reactor Service ====================

// WriteReactorConfig writes a drbd-reactor plugin configuration
func (c *AgentClient) WriteReactorConfig(ctx context.Context, pluginID, configContent string, reloadDaemon bool) (*pb.WriteReactorConfigResponse, error) {
	req := &pb.WriteReactorConfigRequest{
		PluginId:      pluginID,
		ConfigContent: configContent,
		Validate:      true,
		Backup:        true,
		ReloadDaemon:  reloadDaemon,
	}
	return c.drbdReactor.WriteReactorConfig(ctx, req)
}

// ReadReactorConfig reads a drbd-reactor plugin configuration
func (c *AgentClient) ReadReactorConfig(ctx context.Context, pluginID string) (*pb.ReadReactorConfigResponse, error) {
	req := &pb.ReadReactorConfigRequest{
		PluginId: pluginID,
	}
	return c.drbdReactor.ReadReactorConfig(ctx, req)
}

// DeleteReactorConfig deletes a drbd-reactor plugin configuration
func (c *AgentClient) DeleteReactorConfig(ctx context.Context, pluginID string, reloadDaemon bool) (*pb.DeleteReactorConfigResponse, error) {
	req := &pb.DeleteReactorConfigRequest{
		PluginId:     pluginID,
		Backup:       true,
		ReloadDaemon: reloadDaemon,
	}
	return c.drbdReactor.DeleteReactorConfig(ctx, req)
}

// ListReactorConfigs lists all drbd-reactor plugin configurations
func (c *AgentClient) ListReactorConfigs(ctx context.Context) (*pb.ListReactorConfigsResponse, error) {
	req := &pb.ListReactorConfigsRequest{
		IncludeDisabled: true,
	}
	return c.drbdReactor.ListReactorConfigs(ctx, req)
}

// ReactorDaemonReload reloads the drbd-reactor daemon
func (c *AgentClient) ReactorDaemonReload(ctx context.Context, waitForCompletion bool) (*pb.ReactorDaemonReloadResponse, error) {
	req := &pb.ReactorDaemonReloadRequest{
		WaitForCompletion: waitForCompletion,
	}
	return c.drbdReactor.ReactorDaemonReload(ctx, req)
}

// ReactorDaemonStatus gets the drbd-reactor daemon status
func (c *AgentClient) ReactorDaemonStatus(ctx context.Context) (*pb.ReactorDaemonStatusResponse, error) {
	req := &pb.ReactorDaemonStatusRequest{
		IncludePlugins:   true,
		IncludeResources: true,
	}
	return c.drbdReactor.ReactorDaemonStatus(ctx, req)
}

// ReactorStatus gets a specific plugin's status
func (c *AgentClient) ReactorStatus(ctx context.Context, pluginID string) (*pb.ReactorStatusResponse, error) {
	req := &pb.ReactorStatusRequest{
		PluginId: pluginID,
		Verbose:  true,
	}
	return c.drbdReactor.ReactorStatus(ctx, req)
}

// ReactorGetActiveNode gets the active node for a promoter plugin
func (c *AgentClient) ReactorGetActiveNode(ctx context.Context, pluginID string) (*pb.ReactorGetActiveNodeResponse, error) {
	req := &pb.ReactorGetActiveNodeRequest{
		PluginId: pluginID,
	}
	return c.drbdReactor.ReactorGetActiveNode(ctx, req)
}

// PromoterEvict evicts resources from promoter control
func (c *AgentClient) PromoterEvict(ctx context.Context, configs []string, force bool) (*pb.PromoterEvictResponse, error) {
	req := &pb.PromoterEvictRequest{
		Configs: configs,
		Force:   force,
	}
	return c.drbdReactor.PromoterEvict(ctx, req)
}
