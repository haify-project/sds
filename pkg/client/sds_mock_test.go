package client

import (
	"context"
	"net"
	"testing"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

type mockServer struct {
	sdspb.UnimplementedSDSControllerServer
}

func (m *mockServer) RegisterNode(ctx context.Context, req *sdspb.RegisterNodeRequest) (*sdspb.RegisterNodeResponse, error) {
	if req.Name == "error-node" {
		return &sdspb.RegisterNodeResponse{Success: false, Message: "registration failed"}, nil
	}
	return &sdspb.RegisterNodeResponse{
		Success: true,
		Message: "registered",
		Node:    &sdspb.NodeInfo{Name: req.Name, Address: req.Address, State: "online"},
	}, nil
}

func (m *mockServer) UnregisterNode(ctx context.Context, req *sdspb.UnregisterNodeRequest) (*sdspb.UnregisterNodeResponse, error) {
	if req.Address == "fail-addr" {
		return &sdspb.UnregisterNodeResponse{Success: false, Message: "not found"}, nil
	}
	return &sdspb.UnregisterNodeResponse{Success: true, Message: "unregistered"}, nil
}

func (m *mockServer) ListNodes(ctx context.Context, req *sdspb.ListNodesRequest) (*sdspb.ListNodesResponse, error) {
	return &sdspb.ListNodesResponse{
		Success: true,
		Nodes: []*sdspb.NodeInfo{
			{Name: "node1", Address: "10.0.0.1"},
			{Name: "node2", Address: "10.0.0.2"},
		},
	}, nil
}

func (m *mockServer) GetNode(ctx context.Context, req *sdspb.GetNodeRequest) (*sdspb.GetNodeResponse, error) {
	if req.Address == "fail-node" {
		return &sdspb.GetNodeResponse{Success: false, Message: "node not found"}, nil
	}
	return &sdspb.GetNodeResponse{
		Success: true,
		Node:    &sdspb.NodeInfo{Name: "node1", Address: req.Address},
	}, nil
}

func (m *mockServer) SetNodeLabels(ctx context.Context, req *sdspb.SetNodeLabelsRequest) (*sdspb.SetNodeLabelsResponse, error) {
	if req.Node == "fail-node" {
		return &sdspb.SetNodeLabelsResponse{Success: false, Message: "label fail"}, nil
	}
	return &sdspb.SetNodeLabelsResponse{
		Success: true,
		Node:    &sdspb.NodeInfo{Name: req.Node, Labels: req.Labels},
	}, nil
}

func (m *mockServer) DrainNode(ctx context.Context, req *sdspb.DrainNodeRequest) (*sdspb.DrainNodeResponse, error) {
	if req.Name == "fail-node" {
		return &sdspb.DrainNodeResponse{Success: false, Message: "cannot drain"}, nil
	}
	return &sdspb.DrainNodeResponse{Success: true, Message: "drained", ResourcesMoved: []string{"res1"}}, nil
}

func (m *mockServer) UndrainNode(ctx context.Context, req *sdspb.UndrainNodeRequest) (*sdspb.UndrainNodeResponse, error) {
	if req.Name == "fail-node" {
		return &sdspb.UndrainNodeResponse{Success: false, Message: "cannot undrain"}, nil
	}
	return &sdspb.UndrainNodeResponse{Success: true, Message: "undrained"}, nil
}

func (m *mockServer) HealthCheck(ctx context.Context, req *sdspb.HealthCheckRequest) (*sdspb.HealthCheckResponse, error) {
	if req.Node == "fail-node" {
		return &sdspb.HealthCheckResponse{Success: false, Message: "health fail"}, nil
	}
	return &sdspb.HealthCheckResponse{
		Success: true,
		Health:  &sdspb.NodeHealthInfo{DrbdInstalled: true},
	}, nil
}

func (m *mockServer) CreatePool(ctx context.Context, req *sdspb.CreatePoolRequest) (*sdspb.CreatePoolResponse, error) {
	if req.Name == "fail-pool" {
		return &sdspb.CreatePoolResponse{Success: false, Message: "pool exists"}, nil
	}
	return &sdspb.CreatePoolResponse{Success: true, Message: "created"}, nil
}

func (m *mockServer) DeletePool(ctx context.Context, req *sdspb.DeletePoolRequest) (*sdspb.DeletePoolResponse, error) {
	if req.Name == "fail-pool" {
		return &sdspb.DeletePoolResponse{Success: false, Message: "delete fail"}, nil
	}
	return &sdspb.DeletePoolResponse{Success: true, Message: "deleted"}, nil
}

func (m *mockServer) GetPool(ctx context.Context, req *sdspb.GetPoolRequest) (*sdspb.GetPoolResponse, error) {
	if req.Name == "fail-pool" {
		return &sdspb.GetPoolResponse{Success: false, Message: "not found"}, nil
	}
	return &sdspb.GetPoolResponse{
		Success: true,
		Pool:    &sdspb.PoolInfo{Name: req.Name, Type: "lvm"},
	}, nil
}

func (m *mockServer) ListPools(ctx context.Context, req *sdspb.ListPoolsRequest) (*sdspb.ListPoolsResponse, error) {
	return &sdspb.ListPoolsResponse{
		Success: true,
		Pools: []*sdspb.PoolInfo{
			{Name: "pool1", Type: "lvm", Node: "node1", FreeGb: 100},
		},
	}, nil
}

func (m *mockServer) AddDiskToPool(ctx context.Context, req *sdspb.AddDiskToPoolRequest) (*sdspb.AddDiskToPoolResponse, error) {
	if req.Pool == "fail-pool" {
		return &sdspb.AddDiskToPoolResponse{Success: false, Message: "add disk fail"}, nil
	}
	return &sdspb.AddDiskToPoolResponse{Success: true, Message: "disk added"}, nil
}

func (m *mockServer) CreateResource(ctx context.Context, req *sdspb.CreateResourceRequest) (*sdspb.CreateResourceResponse, error) {
	if req.Name == "fail-res" {
		return &sdspb.CreateResourceResponse{Success: false, Message: "resource fail"}, nil
	}
	return &sdspb.CreateResourceResponse{Success: true, Message: "resource created"}, nil
}

func (m *mockServer) DeleteResource(ctx context.Context, req *sdspb.DeleteResourceRequest) (*sdspb.DeleteResourceResponse, error) {
	if req.Name == "fail-res" {
		return &sdspb.DeleteResourceResponse{Success: false, Message: "delete fail"}, nil
	}
	return &sdspb.DeleteResourceResponse{Success: true, Message: "deleted"}, nil
}

func (m *mockServer) GetResource(ctx context.Context, req *sdspb.GetResourceRequest) (*sdspb.GetResourceResponse, error) {
	if req.Name == "fail-res" {
		return &sdspb.GetResourceResponse{Success: false, Message: "not found"}, nil
	}
	return &sdspb.GetResourceResponse{
		Success:  true,
		Resource: &sdspb.ResourceInfo{Name: req.Name, Role: "Primary"},
	}, nil
}

func (m *mockServer) ListResources(ctx context.Context, req *sdspb.ListResourcesRequest) (*sdspb.ListResourcesResponse, error) {
	return &sdspb.ListResourcesResponse{
		Success: true,
		Resources: []*sdspb.ResourceInfo{
			{Name: "res1", Role: "Primary"},
		},
	}, nil
}

func (m *mockServer) CreateResourceProfile(_ context.Context, req *sdspb.CreateResourceProfileRequest) (*sdspb.CreateResourceProfileResponse, error) {
	if req.GetProfile().GetName() == "fail-profile" {
		return &sdspb.CreateResourceProfileResponse{Success: false, Message: "profile invalid"}, nil
	}
	return &sdspb.CreateResourceProfileResponse{Success: true, Profile: req.Profile}, nil
}

func (m *mockServer) GetResourceProfile(_ context.Context, req *sdspb.GetResourceProfileRequest) (*sdspb.GetResourceProfileResponse, error) {
	if req.Name == "missing" {
		return &sdspb.GetResourceProfileResponse{Success: false, Message: "profile not found"}, nil
	}
	return &sdspb.GetResourceProfileResponse{Success: true, Profile: &sdspb.ResourceProfile{Name: req.Name, Pool: "fast"}}, nil
}

func (m *mockServer) ListResourceProfiles(context.Context, *sdspb.ListResourceProfilesRequest) (*sdspb.ListResourceProfilesResponse, error) {
	return &sdspb.ListResourceProfilesResponse{Success: true, Profiles: []*sdspb.ResourceProfile{{Name: "production"}, {Name: "archive"}}}, nil
}

func (m *mockServer) DeleteResourceProfile(_ context.Context, req *sdspb.DeleteResourceProfileRequest) (*sdspb.DeleteResourceProfileResponse, error) {
	if req.Name == "missing" {
		return &sdspb.DeleteResourceProfileResponse{Success: false, Message: "profile not found"}, nil
	}
	return &sdspb.DeleteResourceProfileResponse{Success: true}, nil
}

func (m *mockServer) ResizeVolume(ctx context.Context, req *sdspb.ResizeVolumeRequest) (*sdspb.ResizeVolumeResponse, error) {
	if req.Resource == "fail-res" {
		return &sdspb.ResizeVolumeResponse{Success: false, Message: "resize fail"}, nil
	}
	return &sdspb.ResizeVolumeResponse{Success: true, Message: "volume resized"}, nil
}

func (m *mockServer) SetPrimary(ctx context.Context, req *sdspb.SetPrimaryRequest) (*sdspb.SetPrimaryResponse, error) {
	if req.Resource == "fail-res" {
		return &sdspb.SetPrimaryResponse{Success: false, Message: "set primary fail"}, nil
	}
	return &sdspb.SetPrimaryResponse{Success: true, Message: "primary set"}, nil
}

func (m *mockServer) SetSecondary(ctx context.Context, req *sdspb.SetSecondaryRequest) (*sdspb.SetSecondaryResponse, error) {
	if req.Resource == "fail-res" {
		return &sdspb.SetSecondaryResponse{Success: false, Message: "set secondary fail"}, nil
	}
	return &sdspb.SetSecondaryResponse{Success: true, Message: "secondary set"}, nil
}

func (m *mockServer) AttachDisklessClient(ctx context.Context, req *sdspb.AttachDisklessClientRequest) (*sdspb.AttachDisklessClientResponse, error) {
	if req.Resource == "fail-res" {
		return &sdspb.AttachDisklessClientResponse{Success: false, Message: "attach fail"}, nil
	}
	return &sdspb.AttachDisklessClientResponse{Success: true, Message: "attached"}, nil
}

func (m *mockServer) DetachDisklessClient(ctx context.Context, req *sdspb.DetachDisklessClientRequest) (*sdspb.DetachDisklessClientResponse, error) {
	if req.Resource == "fail-res" {
		return &sdspb.DetachDisklessClientResponse{Success: false, Message: "detach fail"}, nil
	}
	return &sdspb.DetachDisklessClientResponse{Success: true, Message: "detached"}, nil
}

func (m *mockServer) MakeHa(ctx context.Context, req *sdspb.MakeHaRequest) (*sdspb.MakeHaResponse, error) {
	if req.Resource == "fail-res" {
		return &sdspb.MakeHaResponse{Success: false, Message: "ha fail"}, nil
	}
	return &sdspb.MakeHaResponse{Success: true, Message: "ha created", ConfigPath: "/etc/ha"}, nil
}

func (m *mockServer) GetHa(ctx context.Context, req *sdspb.GetHaRequest) (*sdspb.GetHaResponse, error) {
	if req.Resource == "fail-res" {
		return &sdspb.GetHaResponse{Success: false, Message: "not found"}, nil
	}
	return &sdspb.GetHaResponse{
		Success: true,
		Config:  &sdspb.HaConfigInfo{Resource: req.Resource, Vip: "10.0.0.100"},
	}, nil
}

func (m *mockServer) ListHa(ctx context.Context, req *sdspb.ListHaRequest) (*sdspb.ListHaResponse, error) {
	return &sdspb.ListHaResponse{
		Success: true,
		Configs: []*sdspb.HaConfigInfo{{Resource: "res1", Vip: "10.0.0.100"}},
	}, nil
}

func (m *mockServer) GetHaStatus(ctx context.Context, req *sdspb.GetHaStatusRequest) (*sdspb.GetHaStatusResponse, error) {
	return &sdspb.GetHaStatusResponse{
		Success: true,
		Promoters: []*sdspb.HaPromoterStatus{
			{DrbdResource: "res1", PrimaryOn: "node1", Status: "active"},
		},
	}, nil
}

func (m *mockServer) CreateSnapshot(ctx context.Context, req *sdspb.CreateSnapshotRequest) (*sdspb.CreateSnapshotResponse, error) {
	if req.SnapshotName == "fail-snap" {
		return &sdspb.CreateSnapshotResponse{Success: false, Message: "snap fail"}, nil
	}
	return &sdspb.CreateSnapshotResponse{Success: true, Message: "snap created"}, nil
}

func (m *mockServer) ListSnapshots(ctx context.Context, req *sdspb.ListSnapshotsRequest) (*sdspb.ListSnapshotsResponse, error) {
	return &sdspb.ListSnapshotsResponse{
		Success:   true,
		Snapshots: []*sdspb.SnapshotInfo{{Name: "snap1", Volume: "vol1"}},
	}, nil
}

func (m *mockServer) DeleteSnapshot(ctx context.Context, req *sdspb.DeleteSnapshotRequest) (*sdspb.DeleteSnapshotResponse, error) {
	if req.SnapshotName == "fail-snap" {
		return &sdspb.DeleteSnapshotResponse{Success: false, Message: "delete fail"}, nil
	}
	return &sdspb.DeleteSnapshotResponse{Success: true, Message: "deleted"}, nil
}

func (m *mockServer) CreateNFSGateway(ctx context.Context, req *sdspb.CreateNFSGatewayRequest) (*sdspb.CreateNFSGatewayResponse, error) {
	if req.Resource == "fail-res" {
		return &sdspb.CreateNFSGatewayResponse{Success: false, Message: "gateway fail"}, nil
	}
	return &sdspb.CreateNFSGatewayResponse{Success: true, Message: "gateway created"}, nil
}

func (m *mockServer) ListGateways(ctx context.Context, req *sdspb.ListGatewaysRequest) (*sdspb.ListGatewaysResponse, error) {
	return &sdspb.ListGatewaysResponse{
		Success:  true,
		Gateways: []*sdspb.GatewayInfo{{Resource: "res1", Type: "nfs"}},
	}, nil
}

func (m *mockServer) DeleteGateway(ctx context.Context, req *sdspb.DeleteGatewayRequest) (*sdspb.DeleteGatewayResponse, error) {
	if req.Id == "fail-gw" {
		return &sdspb.DeleteGatewayResponse{Success: false, Message: "delete gateway fail"}, nil
	}
	return &sdspb.DeleteGatewayResponse{Success: true, Message: "gateway deleted"}, nil
}

func setupMockClient(t *testing.T) (*SDSClient, func()) {
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	sdspb.RegisterSDSControllerServer(s, &mockServer{})

	go func() {
		if err := s.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			t.Errorf("Server exited with error: %v", err)
		}
	}()

	bufDialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	// NewClient, not the deprecated DialContext: it never blocks on a connection
	// and resolves "passthrough://bufnet" through the custom dialer exactly the
	// same way, so the bufconn wiring is unchanged.
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(bufDialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	client := &SDSClient{
		conn:   conn,
		client: sdspb.NewSDSControllerClient(conn),
	}

	cleanup := func() {
		_ = client.Close()
		s.Stop()
		_ = lis.Close()
	}

	return client, cleanup
}

// Mock implementations for all remaining RPCs to achieve >70% coverage.

func (m *mockServer) CreateResourceAutoPlace(ctx context.Context, req *sdspb.CreateResourceRequest) (*sdspb.CreateResourceResponse, error) {
	return &sdspb.CreateResourceResponse{Success: true, Message: "auto placed"}, nil
}

func (m *mockServer) AdoptResource(ctx context.Context, req *sdspb.AdoptResourceRequest) (*sdspb.AdoptResourceResponse, error) {
	return &sdspb.AdoptResourceResponse{Success: true, Message: "adopted"}, nil
}

func (m *mockServer) ResourceStatus(ctx context.Context, req *sdspb.ResourceStatusRequest) (*sdspb.ResourceStatusResponse, error) {
	return &sdspb.ResourceStatusResponse{Success: true, Status: &sdspb.ResourceStatus{Name: req.Name}}, nil
}

func (m *mockServer) PromoteForNode(ctx context.Context, req *sdspb.SetPrimaryRequest) (*sdspb.SetPrimaryResponse, error) {
	return &sdspb.SetPrimaryResponse{Success: true, Message: "promoted"}, nil
}

func (m *mockServer) AddVolume(ctx context.Context, req *sdspb.AddVolumeRequest) (*sdspb.AddVolumeResponse, error) {
	return &sdspb.AddVolumeResponse{Success: true, Message: "vol added"}, nil
}

func (m *mockServer) RemoveVolume(ctx context.Context, req *sdspb.RemoveVolumeRequest) (*sdspb.RemoveVolumeResponse, error) {
	return &sdspb.RemoveVolumeResponse{Success: true, Message: "vol removed"}, nil
}

func (m *mockServer) UpdateResourceOptions(ctx context.Context, req *sdspb.UpdateResourceOptionsRequest) (*sdspb.UpdateResourceOptionsResponse, error) {
	return &sdspb.UpdateResourceOptionsResponse{Success: true, Message: "updated"}, nil
}

func (m *mockServer) CreateFilesystem(ctx context.Context, req *sdspb.CreateFilesystemRequest) (*sdspb.CreateFilesystemResponse, error) {
	return &sdspb.CreateFilesystemResponse{Success: true, Message: "fs created"}, nil
}

func (m *mockServer) MountResource(ctx context.Context, req *sdspb.MountResourceRequest) (*sdspb.MountResourceResponse, error) {
	return &sdspb.MountResourceResponse{Success: true, Message: "mounted"}, nil
}

func (m *mockServer) UnmountResource(ctx context.Context, req *sdspb.UnmountResourceRequest) (*sdspb.UnmountResourceResponse, error) {
	return &sdspb.UnmountResourceResponse{Success: true, Message: "unmounted"}, nil
}

func (m *mockServer) EnableSelfHa(ctx context.Context, req *sdspb.EnableSelfHaRequest) (*sdspb.EnableSelfHaResponse, error) {
	return &sdspb.EnableSelfHaResponse{Success: true, Message: "self ha enabled"}, nil
}

func (m *mockServer) DisableSelfHa(ctx context.Context, req *sdspb.DisableSelfHaRequest) (*sdspb.DisableSelfHaResponse, error) {
	return &sdspb.DisableSelfHaResponse{Success: true, Message: "self ha disabled"}, nil
}

func (m *mockServer) GetSelfHaStatus(ctx context.Context, req *sdspb.GetSelfHaStatusRequest) (*sdspb.GetSelfHaStatusResponse, error) {
	return &sdspb.GetSelfHaStatusResponse{Success: true, Enabled: true}, nil
}

func (m *mockServer) EvictHa(ctx context.Context, req *sdspb.EvictHaRequest) (*sdspb.EvictHaResponse, error) {
	return &sdspb.EvictHaResponse{Success: true, Message: "evicted"}, nil
}

func (m *mockServer) DeleteHa(ctx context.Context, req *sdspb.DeleteHaRequest) (*sdspb.DeleteHaResponse, error) {
	return &sdspb.DeleteHaResponse{Success: true, Message: "ha deleted"}, nil
}

func (m *mockServer) ListResourceAgents(ctx context.Context, req *sdspb.ListResourceAgentsRequest) (*sdspb.ListResourceAgentsResponse, error) {
	return &sdspb.ListResourceAgentsResponse{Agents: []*sdspb.ResourceAgentInfo{{Name: "agent1"}}}, nil
}

func (m *mockServer) GetResourceAgentMetadata(ctx context.Context, req *sdspb.GetResourceAgentMetadataRequest) (*sdspb.GetResourceAgentMetadataResponse, error) {
	return &sdspb.GetResourceAgentMetadataResponse{}, nil
}

func (m *mockServer) GetHaToml(ctx context.Context, req *sdspb.GetHaTomlRequest) (*sdspb.GetHaTomlResponse, error) {
	return &sdspb.GetHaTomlResponse{Path: "/etc/ha.toml", Content: "toml"}, nil
}

func (m *mockServer) SyncHaToml(ctx context.Context, req *sdspb.SyncHaTomlRequest) (*sdspb.SyncHaTomlResponse, error) {
	return &sdspb.SyncHaTomlResponse{Success: true, Message: "synced"}, nil
}

func (m *mockServer) CreateSnapshotSchedule(ctx context.Context, req *sdspb.CreateSnapshotScheduleRequest) (*sdspb.CreateSnapshotScheduleResponse, error) {
	return &sdspb.CreateSnapshotScheduleResponse{Success: true}, nil
}

func (m *mockServer) ListSnapshotSchedules(ctx context.Context, req *sdspb.ListSnapshotSchedulesRequest) (*sdspb.ListSnapshotSchedulesResponse, error) {
	return &sdspb.ListSnapshotSchedulesResponse{Success: true}, nil
}

func (m *mockServer) DeleteSnapshotSchedule(ctx context.Context, req *sdspb.DeleteSnapshotScheduleRequest) (*sdspb.DeleteSnapshotScheduleResponse, error) {
	return &sdspb.DeleteSnapshotScheduleResponse{Success: true}, nil
}

func (m *mockServer) RestoreSnapshot(ctx context.Context, req *sdspb.RestoreSnapshotRequest) (*sdspb.RestoreSnapshotResponse, error) {
	return &sdspb.RestoreSnapshotResponse{Success: true}, nil
}

func (m *mockServer) CreateISCSIGateway(ctx context.Context, req *sdspb.CreateISCSIGatewayRequest) (*sdspb.CreateISCSIGatewayResponse, error) {
	return &sdspb.CreateISCSIGatewayResponse{Success: true}, nil
}

func (m *mockServer) CreateNVMeGateway(ctx context.Context, req *sdspb.CreateNVMeGatewayRequest) (*sdspb.CreateNVMeGatewayResponse, error) {
	return &sdspb.CreateNVMeGatewayResponse{Success: true}, nil
}

func (m *mockServer) GetGateway(ctx context.Context, req *sdspb.GetGatewayRequest) (*sdspb.GetGatewayResponse, error) {
	return &sdspb.GetGatewayResponse{Success: true, Gateway: &sdspb.GatewayInfo{Id: req.Id}}, nil
}

func (m *mockServer) StartGateway(ctx context.Context, req *sdspb.StartGatewayRequest) (*sdspb.StartGatewayResponse, error) {
	return &sdspb.StartGatewayResponse{Success: true}, nil
}

func (m *mockServer) StopGateway(ctx context.Context, req *sdspb.StopGatewayRequest) (*sdspb.StopGatewayResponse, error) {
	return &sdspb.StopGatewayResponse{Success: true}, nil
}

func (m *mockServer) AddNFSExport(ctx context.Context, req *sdspb.AddNFSExportRequest) (*sdspb.AddNFSExportResponse, error) {
	return &sdspb.AddNFSExportResponse{Success: true}, nil
}

func (m *mockServer) RemoveNFSExport(ctx context.Context, req *sdspb.RemoveNFSExportRequest) (*sdspb.RemoveNFSExportResponse, error) {
	return &sdspb.RemoveNFSExportResponse{Success: true}, nil
}

func (m *mockServer) ListNFSExports(ctx context.Context, req *sdspb.ListNFSExportsRequest) (*sdspb.ListNFSExportsResponse, error) {
	return &sdspb.ListNFSExportsResponse{Success: true}, nil
}

func (m *mockServer) AddISCSILUN(ctx context.Context, req *sdspb.AddISCSILUNRequest) (*sdspb.AddISCSILUNResponse, error) {
	return &sdspb.AddISCSILUNResponse{Success: true}, nil
}

func (m *mockServer) RemoveISCSILUN(ctx context.Context, req *sdspb.RemoveISCSILUNRequest) (*sdspb.RemoveISCSILUNResponse, error) {
	return &sdspb.RemoveISCSILUNResponse{Success: true}, nil
}

func (m *mockServer) ListISCSILUNs(ctx context.Context, req *sdspb.ListISCSILUNsRequest) (*sdspb.ListISCSILUNsResponse, error) {
	return &sdspb.ListISCSILUNsResponse{Success: true}, nil
}

func (m *mockServer) AddISCSIInitiator(ctx context.Context, req *sdspb.AddISCSIInitiatorRequest) (*sdspb.AddISCSIInitiatorResponse, error) {
	return &sdspb.AddISCSIInitiatorResponse{Success: true}, nil
}

func (m *mockServer) RemoveISCSIInitiator(ctx context.Context, req *sdspb.RemoveISCSIInitiatorRequest) (*sdspb.RemoveISCSIInitiatorResponse, error) {
	return &sdspb.RemoveISCSIInitiatorResponse{Success: true}, nil
}

func (m *mockServer) ListISCSIInitiators(ctx context.Context, req *sdspb.ListISCSIInitiatorsRequest) (*sdspb.ListISCSIInitiatorsResponse, error) {
	return &sdspb.ListISCSIInitiatorsResponse{Success: true}, nil
}

func (m *mockServer) SetISCSIChap(ctx context.Context, req *sdspb.SetISCSIChapRequest) (*sdspb.SetISCSIChapResponse, error) {
	return &sdspb.SetISCSIChapResponse{Success: true}, nil
}

func (m *mockServer) GetISCSIChap(ctx context.Context, req *sdspb.GetISCSIChapRequest) (*sdspb.GetISCSIChapResponse, error) {
	return &sdspb.GetISCSIChapResponse{Success: true}, nil
}

func (m *mockServer) AddNVMeNamespace(ctx context.Context, req *sdspb.AddNVMeNamespaceRequest) (*sdspb.AddNVMeNamespaceResponse, error) {
	return &sdspb.AddNVMeNamespaceResponse{Success: true}, nil
}

func (m *mockServer) RemoveNVMeNamespace(ctx context.Context, req *sdspb.RemoveNVMeNamespaceRequest) (*sdspb.RemoveNVMeNamespaceResponse, error) {
	return &sdspb.RemoveNVMeNamespaceResponse{Success: true}, nil
}

func (m *mockServer) ListNVMeNamespaces(ctx context.Context, req *sdspb.ListNVMeNamespacesRequest) (*sdspb.ListNVMeNamespacesResponse, error) {
	return &sdspb.ListNVMeNamespacesResponse{Success: true}, nil
}

func (m *mockServer) AddNVMeHost(ctx context.Context, req *sdspb.AddNVMeHostRequest) (*sdspb.AddNVMeHostResponse, error) {
	return &sdspb.AddNVMeHostResponse{Success: true}, nil
}

func (m *mockServer) RemoveNVMeHost(ctx context.Context, req *sdspb.RemoveNVMeHostRequest) (*sdspb.RemoveNVMeHostResponse, error) {
	return &sdspb.RemoveNVMeHostResponse{Success: true}, nil
}

func (m *mockServer) ListNVMeHosts(ctx context.Context, req *sdspb.ListNVMeHostsRequest) (*sdspb.ListNVMeHostsResponse, error) {
	return &sdspb.ListNVMeHostsResponse{Success: true}, nil
}

func (m *mockServer) CreateZFSPool(ctx context.Context, req *sdspb.CreateZFSPoolRequest) (*sdspb.CreateZFSPoolResponse, error) {
	return &sdspb.CreateZFSPoolResponse{Success: true}, nil
}

func (m *mockServer) DeleteZFSPool(ctx context.Context, req *sdspb.DeleteZFSPoolRequest) (*sdspb.DeleteZFSPoolResponse, error) {
	return &sdspb.DeleteZFSPoolResponse{Success: true}, nil
}

func (m *mockServer) ListZFSpools(ctx context.Context, req *sdspb.ListZFSPoolsRequest) (*sdspb.ListZFSPoolsResponse, error) {
	return &sdspb.ListZFSPoolsResponse{Success: true}, nil
}

func (m *mockServer) CreateZFSDataset(ctx context.Context, req *sdspb.CreateZFSDatasetRequest) (*sdspb.CreateZFSDatasetResponse, error) {
	return &sdspb.CreateZFSDatasetResponse{Success: true}, nil
}

func (m *mockServer) DeleteZFSDataset(ctx context.Context, req *sdspb.DeleteZFSDatasetRequest) (*sdspb.DeleteZFSDatasetResponse, error) {
	return &sdspb.DeleteZFSDatasetResponse{Success: true}, nil
}

func (m *mockServer) CreateZFSVolume(ctx context.Context, req *sdspb.CreateZFSVolumeRequest) (*sdspb.CreateZFSVolumeResponse, error) {
	return &sdspb.CreateZFSVolumeResponse{Success: true}, nil
}

func (m *mockServer) ResizeZFSVolume(ctx context.Context, req *sdspb.ResizeZFSVolumeRequest) (*sdspb.ResizeZFSVolumeResponse, error) {
	return &sdspb.ResizeZFSVolumeResponse{Success: true}, nil
}

func (m *mockServer) CreateZFSSnapshot(ctx context.Context, req *sdspb.CreateZFSSnapshotRequest) (*sdspb.CreateZFSSnapshotResponse, error) {
	return &sdspb.CreateZFSSnapshotResponse{Success: true}, nil
}

func (m *mockServer) DeleteZFSSnapshot(ctx context.Context, req *sdspb.DeleteZFSSnapshotRequest) (*sdspb.DeleteZFSSnapshotResponse, error) {
	return &sdspb.DeleteZFSSnapshotResponse{Success: true}, nil
}

func (m *mockServer) ListZFSSnapshots(ctx context.Context, req *sdspb.ListZFSSnapshotsRequest) (*sdspb.ListZFSSnapshotsResponse, error) {
	return &sdspb.ListZFSSnapshotsResponse{Success: true}, nil
}

func (m *mockServer) RestoreZFSSnapshot(ctx context.Context, req *sdspb.RestoreZFSSnapshotRequest) (*sdspb.RestoreZFSSnapshotResponse, error) {
	return &sdspb.RestoreZFSSnapshotResponse{Success: true}, nil
}

func (m *mockServer) CloneZFSSnapshot(ctx context.Context, req *sdspb.CloneZFSSnapshotRequest) (*sdspb.CloneZFSSnapshotResponse, error) {
	return &sdspb.CloneZFSSnapshotResponse{Success: true}, nil
}

func (m *mockServer) CreateLvmSnapshot(ctx context.Context, req *sdspb.CreateLvmSnapshotRequest) (*sdspb.CreateLvmSnapshotResponse, error) {
	return &sdspb.CreateLvmSnapshotResponse{Success: true}, nil
}

func (m *mockServer) DeleteLvmSnapshot(ctx context.Context, req *sdspb.DeleteLvmSnapshotRequest) (*sdspb.DeleteLvmSnapshotResponse, error) {
	return &sdspb.DeleteLvmSnapshotResponse{Success: true}, nil
}

func (m *mockServer) ListLvmSnapshots(ctx context.Context, req *sdspb.ListLvmSnapshotsRequest) (*sdspb.ListLvmSnapshotsResponse, error) {
	return &sdspb.ListLvmSnapshotsResponse{Success: true}, nil
}

func (m *mockServer) RestoreLvmSnapshot(ctx context.Context, req *sdspb.RestoreLvmSnapshotRequest) (*sdspb.RestoreLvmSnapshotResponse, error) {
	return &sdspb.RestoreLvmSnapshotResponse{Success: true}, nil
}
