package mcpserver

import (
	"context"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
)

// ControllerClient is the subset of *client.SDSClient the MCP server uses.
// Tools depend on this interface so tests can substitute a mock without a
// running controller.
type ControllerClient interface {
	// Nodes
	ListNodes(ctx context.Context) ([]*sdspb.NodeInfo, error)
	RegisterNode(ctx context.Context, name, address string) (*sdspb.NodeInfo, error)
	UnregisterNode(ctx context.Context, address string) error
	HealthCheck(ctx context.Context, node string) (*client.NodeHealthInfo, error)

	// Pools
	ListPools(ctx context.Context) ([]*sdspb.PoolInfo, error)
	CreatePool(ctx context.Context, name, poolType, node string, disks []string, sizeGB uint64) error
	CreateZFSPool(ctx context.Context, name, node string, vdevs []string) error
	DeletePool(ctx context.Context, pool, node string) error
	AddDiskToPool(ctx context.Context, pool, disk, node string) error

	// Resources
	ListResources(ctx context.Context) ([]*sdspb.ResourceInfo, error)
	ResourceStatus(ctx context.Context, name string) (*sdspb.ResourceStatus, error)
	CreateResourceWithPoolAndType(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, storageType string, drbdOptions map[string]string) error
	CreateResourceWithVolumes(ctx context.Context, name string, port uint32, nodes []string, protocol, storageType string, drbdOptions map[string]string, volumes []*sdspb.VolumeSpec) error
	AdoptResource(ctx context.Context, name string, nodes []string, port uint32, protocol string) (*sdspb.AdoptResourceResponse, error)
	DeleteResource(ctx context.Context, name string) error
	SetPrimary(ctx context.Context, resource, node string, force bool) error
	PromoteForNode(ctx context.Context, resource, node string) error
	SetSecondary(ctx context.Context, resource, node string) error
	SetDualPrimary(ctx context.Context, resource string, enable bool) error
	AddVolume(ctx context.Context, resource, volume, pool string, sizeGB uint32) error
	UpdateResourceOptions(ctx context.Context, resource string, options map[string]string) error
	RemoveVolume(ctx context.Context, resource string, volumeID uint32) error
	ResizeVolume(ctx context.Context, resource string, volumeID uint32, sizeGB uint32) error
	CreateFilesystem(ctx context.Context, resource string, volumeID uint32, node, fstype string) error
	MountResource(ctx context.Context, resource string, volumeID uint32, path, node, fstype string) error
	UnmountResource(ctx context.Context, resource string, volumeID uint32, node string) error

	// Snapshots (storage-type aware)
	CreateLvmSnapshot(ctx context.Context, pool, lvName, snapshotName, node, size string) error
	ListLvmSnapshots(ctx context.Context, pool, node string) ([]*sdspb.SnapshotInfo, error)
	DeleteLvmSnapshot(ctx context.Context, pool, snapshotName, node string) error
	RestoreLvmSnapshot(ctx context.Context, pool, snapshotName, node string) error
	CreateZFSSnapshot(ctx context.Context, dataset, snapshotName, node string) error
	ListZFSSnapshots(ctx context.Context, dataset, node string) ([]*sdspb.SnapshotInfo, error)
	DeleteZFSSnapshot(ctx context.Context, snapshot, node string) error
	RestoreZFSSnapshot(ctx context.Context, dataset, snapshotName, node string) error

	// Snapshot schedules (cron-driven, GFS retention)
	CreateSnapshotSchedule(ctx context.Context, resource, cron string, keep *sdspb.GFSRetention, enabled bool) error
	ListSnapshotSchedules(ctx context.Context) ([]*sdspb.SnapshotScheduleInfo, error)
	DeleteSnapshotSchedule(ctx context.Context, name string) error

	// Gateways
	ListGateways(ctx context.Context) ([]*sdspb.GatewayInfo, error)
	GetGateway(ctx context.Context, id string) (*sdspb.GatewayInfo, error)
	CreateNFSGateway(ctx context.Context, req *sdspb.CreateNFSGatewayRequest) (*sdspb.CreateNFSGatewayResponse, error)
	CreateISCSIGateway(ctx context.Context, req *sdspb.CreateISCSIGatewayRequest) (*sdspb.CreateISCSIGatewayResponse, error)
	CreateNVMeGateway(ctx context.Context, req *sdspb.CreateNVMeGatewayRequest) (*sdspb.CreateNVMeGatewayResponse, error)
	StartGateway(ctx context.Context, id string) error
	StopGateway(ctx context.Context, id string) error
	DeleteGateway(ctx context.Context, id string) error

	// NFS exports
	AddNFSExport(ctx context.Context, resource, exportPath string, fsid int32, clientSpec, options string) error
	RemoveNFSExport(ctx context.Context, resource, exportPath string) error
	ListNFSExports(ctx context.Context, resource string) ([]*sdspb.NFSExportInfo, error)

	// iSCSI configuration
	AddISCSILUN(ctx context.Context, resource string, lun int32, device string) error
	RemoveISCSILUN(ctx context.Context, resource string, lun int32) error
	ListISCSILUNs(ctx context.Context, resource string) ([]*sdspb.ISCSILUNInfo, error)
	AddISCSIInitiator(ctx context.Context, resource, initiator string) error
	RemoveISCSIInitiator(ctx context.Context, resource, initiator string) error
	ListISCSIInitiators(ctx context.Context, resource string) ([]string, error)
	SetISCSIChap(ctx context.Context, resource, username, password string, mutual bool) error
	GetISCSIChap(ctx context.Context, resource string) (*sdspb.GetISCSIChapResponse, error)

	// NVMe-oF configuration
	AddNVMeNamespace(ctx context.Context, resource, device string) error
	RemoveNVMeNamespace(ctx context.Context, resource string, namespaceID int32) error
	ListNVMeNamespaces(ctx context.Context, resource string) ([]*sdspb.NVMeNamespaceInfo, error)
	AddNVMeHost(ctx context.Context, resource, hostNQN string) error
	RemoveNVMeHost(ctx context.Context, resource, hostNQN string) error
	ListNVMeHosts(ctx context.Context, resource string) ([]string, error)

	// HA
	MakeHa(ctx context.Context, resource string, services []string, mountPoint, fsType, vip string, ocfAgents []*sdspb.OcfAgent, startItems []*sdspb.HaStartItem) (string, error)
	ListHa(ctx context.Context) ([]*sdspb.HaConfigInfo, error)
	GetHa(ctx context.Context, resource string) (*sdspb.HaConfigInfo, error)
	EvictHa(ctx context.Context, resource string) error
	DeleteHa(ctx context.Context, resource string) error
	GetSelfHaStatus(ctx context.Context) (*client.SelfHaStatus, error)
	EnableSelfHa(ctx context.Context, vip, pool string, sizeGB, port uint32, nodes []string) (string, string, error)
	DisableSelfHa(ctx context.Context, node string) error
}

// compile-time check: the real gRPC client satisfies the interface.
var _ ControllerClient = (*client.SDSClient)(nil)
