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
	ListLvmSnapshots(ctx context.Context, pool, node, resource string) ([]*sdspb.SnapshotInfo, error)
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
	GetHaStatus(ctx context.Context, resource string) ([]*sdspb.HaPromoterStatus, error)
	GetHaToml(ctx context.Context, resource string) (*sdspb.GetHaTomlResponse, error)
	SyncHaToml(ctx context.Context, resource, content string) (string, error)
	ListResourceAgents(ctx context.Context) ([]*sdspb.ResourceAgentInfo, error)
	GetResourceAgentMetadata(ctx context.Context, provider, name string) (*sdspb.GetResourceAgentMetadataResponse, error)

	// Observability: what the cluster did, not just what it is. An assistant
	// with only state queries can describe the cluster but cannot explain how
	// it got there — which is most of what an operator actually asks.
	ListEvents(ctx context.Context, req *sdspb.ListEventsRequest) (*sdspb.ListEventsResponse, error)
	ListAuditEvents(ctx context.Context, req *sdspb.ListAuditEventsRequest) (*sdspb.ListAuditEventsResponse, error)
	ListControllerLogs(ctx context.Context, req *sdspb.ListControllerLogsRequest) (*sdspb.ListControllerLogsResponse, error)

	// Notification channels. Listing and testing are exposed; creating one is
	// not, for the same reason a backup target cannot be created from here — a
	// bot URL is a bearer credential, and anything passed as a tool argument is
	// recorded in the conversation that passed it.
	ListNotifyChannels(ctx context.Context) ([]*sdspb.NotifyChannelInfo, []string, error)
	TestNotifyChannel(ctx context.Context, name string) (string, error)

	// Topology changes
	AddReplica(ctx context.Context, resource, node string) error
	RemoveReplica(ctx context.Context, resource, node string) error
	AttachDisklessClient(ctx context.Context, resource, node string) error
	DetachDisklessClient(ctx context.Context, resource, node string) error
	SetTiebreaker(ctx context.Context, resource, node string) (string, string, error)
	AddDR(ctx context.Context, resource, drNode, drEndpoint string, wanPort uint32, egressAddress string) (uint32, error)
	RepairWanProxy(ctx context.Context, name string, dryRun bool) (*sdspb.RepairWanProxyResponse, error)

	// Node lifecycle
	DrainNode(ctx context.Context, name string) ([]string, error)
	UndrainNode(ctx context.Context, name string) error
	SetNodeLabels(ctx context.Context, node string, labels map[string]string, replace bool) (*sdspb.NodeInfo, error)
	ConvertPoolToThin(ctx context.Context, node, pool string) error

	// ZFS
	ListZFSpools(ctx context.Context) ([]*sdspb.PoolInfo, error)
	DeleteZFSPool(ctx context.Context, name, node string) error
	CreateZFSDataset(ctx context.Context, datasetPath, node string) error
	DeleteZFSDataset(ctx context.Context, datasetPath, node string) error
	CreateZFSVolume(ctx context.Context, poolName, volumeName, size, node string) error
	ResizeZFSVolume(ctx context.Context, volumePath, newSize, node string) error
	CloneZFSSnapshot(ctx context.Context, snapshot, clonePath, node string) error

	// Fast tier
	AddPoolCache(ctx context.Context, node, pool, device, mode string) (string, uint64, error)
	RemovePoolCache(ctx context.Context, node, pool string) error

	// Off-cluster backups. AddBackupTarget is absent on purpose: it carries a
	// credential, and MCP tool arguments are recorded by the caller.
	ListBackupTargets(ctx context.Context) ([]*sdspb.BackupTargetInfo, error)
	DeleteBackupTarget(ctx context.Context, name string, force bool) error
	CreateBackup(ctx context.Context, resource, target, node string) (*sdspb.BackupInfo, error)
	ListBackups(ctx context.Context, resource, target string) ([]*sdspb.BackupInfo, error)
	RestoreBackup(ctx context.Context, id, resource, node string) (*sdspb.BackupInfo, error)
	DeleteBackup(ctx context.Context, id, node string, force bool) error
}

// compile-time check: the real gRPC client satisfies the interface.
var _ ControllerClient = (*client.SDSClient)(nil)
