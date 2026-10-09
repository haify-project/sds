package client

import (
	"context"
	"net"
	"testing"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

type mockServer struct {
	haifypb.UnimplementedHaifyControllerServer
}

func (m *mockServer) RegisterNode(ctx context.Context, req *haifypb.RegisterNodeRequest) (*haifypb.RegisterNodeResponse, error) {
	if req.Name == "error-node" {
		return &haifypb.RegisterNodeResponse{Success: false, Message: "registration failed"}, nil
	}
	return &haifypb.RegisterNodeResponse{
		Success: true,
		Message: "registered",
		Node:    &haifypb.NodeInfo{Name: req.Name, Address: req.Address, State: "online"},
	}, nil
}

func (m *mockServer) UnregisterNode(ctx context.Context, req *haifypb.UnregisterNodeRequest) (*haifypb.UnregisterNodeResponse, error) {
	if req.Address == "fail-addr" {
		return &haifypb.UnregisterNodeResponse{Success: false, Message: "not found"}, nil
	}
	return &haifypb.UnregisterNodeResponse{Success: true, Message: "unregistered"}, nil
}

func (m *mockServer) ListNodes(ctx context.Context, req *haifypb.ListNodesRequest) (*haifypb.ListNodesResponse, error) {
	return &haifypb.ListNodesResponse{
		Success: true,
		Nodes: []*haifypb.NodeInfo{
			{Name: "node1", Address: "10.0.0.1"},
			{Name: "node2", Address: "10.0.0.2"},
		},
	}, nil
}

func (m *mockServer) GetNode(ctx context.Context, req *haifypb.GetNodeRequest) (*haifypb.GetNodeResponse, error) {
	if req.Address == "fail-node" {
		return &haifypb.GetNodeResponse{Success: false, Message: "node not found"}, nil
	}
	return &haifypb.GetNodeResponse{
		Success: true,
		Node:    &haifypb.NodeInfo{Name: "node1", Address: req.Address},
	}, nil
}

func (m *mockServer) SetNodeLabels(ctx context.Context, req *haifypb.SetNodeLabelsRequest) (*haifypb.SetNodeLabelsResponse, error) {
	if req.Node == "fail-node" {
		return &haifypb.SetNodeLabelsResponse{Success: false, Message: "label fail"}, nil
	}
	return &haifypb.SetNodeLabelsResponse{
		Success: true,
		Node:    &haifypb.NodeInfo{Name: req.Node, Labels: req.Labels},
	}, nil
}

func (m *mockServer) DrainNode(ctx context.Context, req *haifypb.DrainNodeRequest) (*haifypb.DrainNodeResponse, error) {
	if req.Name == "fail-node" {
		return &haifypb.DrainNodeResponse{Success: false, Message: "cannot drain"}, nil
	}
	return &haifypb.DrainNodeResponse{Success: true, Message: "drained", ResourcesMoved: []string{"res1"}}, nil
}

func (m *mockServer) UndrainNode(ctx context.Context, req *haifypb.UndrainNodeRequest) (*haifypb.UndrainNodeResponse, error) {
	if req.Name == "fail-node" {
		return &haifypb.UndrainNodeResponse{Success: false, Message: "cannot undrain"}, nil
	}
	return &haifypb.UndrainNodeResponse{Success: true, Message: "undrained"}, nil
}

func (m *mockServer) HealthCheck(ctx context.Context, req *haifypb.HealthCheckRequest) (*haifypb.HealthCheckResponse, error) {
	if req.Node == "fail-node" {
		return &haifypb.HealthCheckResponse{Success: false, Message: "health fail"}, nil
	}
	return &haifypb.HealthCheckResponse{
		Success: true,
		Health:  &haifypb.NodeHealthInfo{DrbdInstalled: true},
	}, nil
}

func (m *mockServer) CreatePool(ctx context.Context, req *haifypb.CreatePoolRequest) (*haifypb.CreatePoolResponse, error) {
	if req.Name == "fail-pool" {
		return &haifypb.CreatePoolResponse{Success: false, Message: "pool exists"}, nil
	}
	return &haifypb.CreatePoolResponse{Success: true, Message: "created"}, nil
}

func (m *mockServer) DeletePool(ctx context.Context, req *haifypb.DeletePoolRequest) (*haifypb.DeletePoolResponse, error) {
	if req.Name == "fail-pool" {
		return &haifypb.DeletePoolResponse{Success: false, Message: "delete fail"}, nil
	}
	return &haifypb.DeletePoolResponse{Success: true, Message: "deleted"}, nil
}

func (m *mockServer) GetPool(ctx context.Context, req *haifypb.GetPoolRequest) (*haifypb.GetPoolResponse, error) {
	if req.Name == "fail-pool" {
		return &haifypb.GetPoolResponse{Success: false, Message: "not found"}, nil
	}
	return &haifypb.GetPoolResponse{
		Success: true,
		Pool:    &haifypb.PoolInfo{Name: req.Name, Type: "lvm"},
	}, nil
}

func (m *mockServer) ListPools(ctx context.Context, req *haifypb.ListPoolsRequest) (*haifypb.ListPoolsResponse, error) {
	return &haifypb.ListPoolsResponse{
		Success: true,
		Pools: []*haifypb.PoolInfo{
			{Name: "pool1", Type: "lvm", Node: "node1", FreeGb: 100},
		},
	}, nil
}

func (m *mockServer) AddDiskToPool(ctx context.Context, req *haifypb.AddDiskToPoolRequest) (*haifypb.AddDiskToPoolResponse, error) {
	if req.Pool == "fail-pool" {
		return &haifypb.AddDiskToPoolResponse{Success: false, Message: "add disk fail"}, nil
	}
	return &haifypb.AddDiskToPoolResponse{Success: true, Message: "disk added"}, nil
}

func (m *mockServer) CreateResource(ctx context.Context, req *haifypb.CreateResourceRequest) (*haifypb.CreateResourceResponse, error) {
	if req.Name == "fail-res" {
		return &haifypb.CreateResourceResponse{Success: false, Message: "resource fail"}, nil
	}
	return &haifypb.CreateResourceResponse{Success: true, Message: "resource created"}, nil
}

func (m *mockServer) DeleteResource(ctx context.Context, req *haifypb.DeleteResourceRequest) (*haifypb.DeleteResourceResponse, error) {
	if req.Name == "fail-res" {
		return &haifypb.DeleteResourceResponse{Success: false, Message: "delete fail"}, nil
	}
	return &haifypb.DeleteResourceResponse{Success: true, Message: "deleted"}, nil
}

func (m *mockServer) GetResource(ctx context.Context, req *haifypb.GetResourceRequest) (*haifypb.GetResourceResponse, error) {
	if req.Name == "fail-res" {
		return &haifypb.GetResourceResponse{Success: false, Message: "not found"}, nil
	}
	return &haifypb.GetResourceResponse{
		Success:  true,
		Resource: &haifypb.ResourceInfo{Name: req.Name, Role: "Primary"},
	}, nil
}

func (m *mockServer) ListResources(ctx context.Context, req *haifypb.ListResourcesRequest) (*haifypb.ListResourcesResponse, error) {
	return &haifypb.ListResourcesResponse{
		Success: true,
		Resources: []*haifypb.ResourceInfo{
			{Name: "res1", Role: "Primary"},
		},
	}, nil
}

func (m *mockServer) CreateResourceProfile(_ context.Context, req *haifypb.CreateResourceProfileRequest) (*haifypb.CreateResourceProfileResponse, error) {
	if req.GetProfile().GetName() == "fail-profile" {
		return &haifypb.CreateResourceProfileResponse{Success: false, Message: "profile invalid"}, nil
	}
	return &haifypb.CreateResourceProfileResponse{Success: true, Profile: req.Profile}, nil
}

func (m *mockServer) GetResourceProfile(_ context.Context, req *haifypb.GetResourceProfileRequest) (*haifypb.GetResourceProfileResponse, error) {
	if req.Name == "missing" {
		return &haifypb.GetResourceProfileResponse{Success: false, Message: "profile not found"}, nil
	}
	return &haifypb.GetResourceProfileResponse{Success: true, Profile: &haifypb.ResourceProfile{Name: req.Name, Pool: "fast"}}, nil
}

func (m *mockServer) ListResourceProfiles(context.Context, *haifypb.ListResourceProfilesRequest) (*haifypb.ListResourceProfilesResponse, error) {
	return &haifypb.ListResourceProfilesResponse{Success: true, Profiles: []*haifypb.ResourceProfile{{Name: "production"}, {Name: "archive"}}}, nil
}

func (m *mockServer) DeleteResourceProfile(_ context.Context, req *haifypb.DeleteResourceProfileRequest) (*haifypb.DeleteResourceProfileResponse, error) {
	if req.Name == "missing" {
		return &haifypb.DeleteResourceProfileResponse{Success: false, Message: "profile not found"}, nil
	}
	return &haifypb.DeleteResourceProfileResponse{Success: true}, nil
}

func (m *mockServer) ResizeVolume(ctx context.Context, req *haifypb.ResizeVolumeRequest) (*haifypb.ResizeVolumeResponse, error) {
	if req.Resource == "fail-res" {
		return &haifypb.ResizeVolumeResponse{Success: false, Message: "resize fail"}, nil
	}
	return &haifypb.ResizeVolumeResponse{Success: true, Message: "volume resized"}, nil
}

func (m *mockServer) SetPrimary(ctx context.Context, req *haifypb.SetPrimaryRequest) (*haifypb.SetPrimaryResponse, error) {
	if req.Resource == "fail-res" {
		return &haifypb.SetPrimaryResponse{Success: false, Message: "set primary fail"}, nil
	}
	return &haifypb.SetPrimaryResponse{Success: true, Message: "primary set"}, nil
}

func (m *mockServer) SetSecondary(ctx context.Context, req *haifypb.SetSecondaryRequest) (*haifypb.SetSecondaryResponse, error) {
	if req.Resource == "fail-res" {
		return &haifypb.SetSecondaryResponse{Success: false, Message: "set secondary fail"}, nil
	}
	return &haifypb.SetSecondaryResponse{Success: true, Message: "secondary set"}, nil
}

func (m *mockServer) AttachDisklessClient(ctx context.Context, req *haifypb.AttachDisklessClientRequest) (*haifypb.AttachDisklessClientResponse, error) {
	if req.Resource == "fail-res" {
		return &haifypb.AttachDisklessClientResponse{Success: false, Message: "attach fail"}, nil
	}
	return &haifypb.AttachDisklessClientResponse{Success: true, Message: "attached"}, nil
}

func (m *mockServer) DetachDisklessClient(ctx context.Context, req *haifypb.DetachDisklessClientRequest) (*haifypb.DetachDisklessClientResponse, error) {
	if req.Resource == "fail-res" {
		return &haifypb.DetachDisklessClientResponse{Success: false, Message: "detach fail"}, nil
	}
	return &haifypb.DetachDisklessClientResponse{Success: true, Message: "detached"}, nil
}

func (m *mockServer) MakeHa(ctx context.Context, req *haifypb.MakeHaRequest) (*haifypb.MakeHaResponse, error) {
	if req.Resource == "fail-res" {
		return &haifypb.MakeHaResponse{Success: false, Message: "ha fail"}, nil
	}
	return &haifypb.MakeHaResponse{Success: true, Message: "ha created", ConfigPath: "/etc/ha"}, nil
}

func (m *mockServer) GetHa(ctx context.Context, req *haifypb.GetHaRequest) (*haifypb.GetHaResponse, error) {
	if req.Resource == "fail-res" {
		return &haifypb.GetHaResponse{Success: false, Message: "not found"}, nil
	}
	return &haifypb.GetHaResponse{
		Success: true,
		Config:  &haifypb.HaConfigInfo{Resource: req.Resource, Vip: "10.0.0.100"},
	}, nil
}

func (m *mockServer) ListHa(ctx context.Context, req *haifypb.ListHaRequest) (*haifypb.ListHaResponse, error) {
	return &haifypb.ListHaResponse{
		Success: true,
		Configs: []*haifypb.HaConfigInfo{{Resource: "res1", Vip: "10.0.0.100"}},
	}, nil
}

func (m *mockServer) GetHaStatus(ctx context.Context, req *haifypb.GetHaStatusRequest) (*haifypb.GetHaStatusResponse, error) {
	return &haifypb.GetHaStatusResponse{
		Success: true,
		Promoters: []*haifypb.HaPromoterStatus{
			{DrbdResource: "res1", PrimaryOn: "node1", Status: "active"},
		},
	}, nil
}

func (m *mockServer) CreateSnapshot(ctx context.Context, req *haifypb.CreateSnapshotRequest) (*haifypb.CreateSnapshotResponse, error) {
	if req.SnapshotName == "fail-snap" {
		return &haifypb.CreateSnapshotResponse{Success: false, Message: "snap fail"}, nil
	}
	return &haifypb.CreateSnapshotResponse{Success: true, Message: "snap created"}, nil
}

func (m *mockServer) ListSnapshots(ctx context.Context, req *haifypb.ListSnapshotsRequest) (*haifypb.ListSnapshotsResponse, error) {
	return &haifypb.ListSnapshotsResponse{
		Success:   true,
		Snapshots: []*haifypb.SnapshotInfo{{Name: "snap1", Volume: "vol1"}},
	}, nil
}

func (m *mockServer) DeleteSnapshot(ctx context.Context, req *haifypb.DeleteSnapshotRequest) (*haifypb.DeleteSnapshotResponse, error) {
	if req.SnapshotName == "fail-snap" {
		return &haifypb.DeleteSnapshotResponse{Success: false, Message: "delete fail"}, nil
	}
	return &haifypb.DeleteSnapshotResponse{Success: true, Message: "deleted"}, nil
}

func (m *mockServer) CreateNFSGateway(ctx context.Context, req *haifypb.CreateNFSGatewayRequest) (*haifypb.CreateNFSGatewayResponse, error) {
	if req.Resource == "fail-res" {
		return &haifypb.CreateNFSGatewayResponse{Success: false, Message: "gateway fail"}, nil
	}
	return &haifypb.CreateNFSGatewayResponse{Success: true, Message: "gateway created"}, nil
}

func (m *mockServer) ListGateways(ctx context.Context, req *haifypb.ListGatewaysRequest) (*haifypb.ListGatewaysResponse, error) {
	return &haifypb.ListGatewaysResponse{
		Success:  true,
		Gateways: []*haifypb.GatewayInfo{{Resource: "res1", Type: "nfs"}},
	}, nil
}

func (m *mockServer) DeleteGateway(ctx context.Context, req *haifypb.DeleteGatewayRequest) (*haifypb.DeleteGatewayResponse, error) {
	if req.Id == "fail-gw" {
		return &haifypb.DeleteGatewayResponse{Success: false, Message: "delete gateway fail"}, nil
	}
	return &haifypb.DeleteGatewayResponse{Success: true, Message: "gateway deleted"}, nil
}

func setupMockClient(t *testing.T) (*HaifyClient, func()) {
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	haifypb.RegisterHaifyControllerServer(s, &mockServer{})

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

	client := &HaifyClient{
		conn:   conn,
		client: haifypb.NewHaifyControllerClient(conn),
	}

	cleanup := func() {
		_ = client.Close()
		s.Stop()
		_ = lis.Close()
	}

	return client, cleanup
}

// Mock implementations for all remaining RPCs to achieve >70% coverage.

func (m *mockServer) CreateResourceAutoPlace(ctx context.Context, req *haifypb.CreateResourceRequest) (*haifypb.CreateResourceResponse, error) {
	return &haifypb.CreateResourceResponse{Success: true, Message: "auto placed"}, nil
}

func (m *mockServer) AdoptResource(ctx context.Context, req *haifypb.AdoptResourceRequest) (*haifypb.AdoptResourceResponse, error) {
	return &haifypb.AdoptResourceResponse{Success: true, Message: "adopted"}, nil
}

func (m *mockServer) ResourceStatus(ctx context.Context, req *haifypb.ResourceStatusRequest) (*haifypb.ResourceStatusResponse, error) {
	return &haifypb.ResourceStatusResponse{Success: true, Status: &haifypb.ResourceStatus{Name: req.Name}}, nil
}

func (m *mockServer) PromoteForNode(ctx context.Context, req *haifypb.SetPrimaryRequest) (*haifypb.SetPrimaryResponse, error) {
	return &haifypb.SetPrimaryResponse{Success: true, Message: "promoted"}, nil
}

func (m *mockServer) AddVolume(ctx context.Context, req *haifypb.AddVolumeRequest) (*haifypb.AddVolumeResponse, error) {
	return &haifypb.AddVolumeResponse{Success: true, Message: "vol added"}, nil
}

func (m *mockServer) RemoveVolume(ctx context.Context, req *haifypb.RemoveVolumeRequest) (*haifypb.RemoveVolumeResponse, error) {
	return &haifypb.RemoveVolumeResponse{Success: true, Message: "vol removed"}, nil
}

func (m *mockServer) UpdateResourceOptions(ctx context.Context, req *haifypb.UpdateResourceOptionsRequest) (*haifypb.UpdateResourceOptionsResponse, error) {
	return &haifypb.UpdateResourceOptionsResponse{Success: true, Message: "updated"}, nil
}

func (m *mockServer) CreateFilesystem(ctx context.Context, req *haifypb.CreateFilesystemRequest) (*haifypb.CreateFilesystemResponse, error) {
	return &haifypb.CreateFilesystemResponse{Success: true, Message: "fs created"}, nil
}

func (m *mockServer) MountResource(ctx context.Context, req *haifypb.MountResourceRequest) (*haifypb.MountResourceResponse, error) {
	return &haifypb.MountResourceResponse{Success: true, Message: "mounted"}, nil
}

func (m *mockServer) UnmountResource(ctx context.Context, req *haifypb.UnmountResourceRequest) (*haifypb.UnmountResourceResponse, error) {
	return &haifypb.UnmountResourceResponse{Success: true, Message: "unmounted"}, nil
}

func (m *mockServer) EnableSelfHa(ctx context.Context, req *haifypb.EnableSelfHaRequest) (*haifypb.EnableSelfHaResponse, error) {
	return &haifypb.EnableSelfHaResponse{Success: true, Message: "self ha enabled"}, nil
}

func (m *mockServer) DisableSelfHa(ctx context.Context, req *haifypb.DisableSelfHaRequest) (*haifypb.DisableSelfHaResponse, error) {
	return &haifypb.DisableSelfHaResponse{Success: true, Message: "self ha disabled"}, nil
}

func (m *mockServer) GetSelfHaStatus(ctx context.Context, req *haifypb.GetSelfHaStatusRequest) (*haifypb.GetSelfHaStatusResponse, error) {
	return &haifypb.GetSelfHaStatusResponse{Success: true, Enabled: true}, nil
}

func (m *mockServer) EvictHa(ctx context.Context, req *haifypb.EvictHaRequest) (*haifypb.EvictHaResponse, error) {
	return &haifypb.EvictHaResponse{Success: true, Message: "evicted"}, nil
}

func (m *mockServer) DeleteHa(ctx context.Context, req *haifypb.DeleteHaRequest) (*haifypb.DeleteHaResponse, error) {
	return &haifypb.DeleteHaResponse{Success: true, Message: "ha deleted"}, nil
}

func (m *mockServer) ListResourceAgents(ctx context.Context, req *haifypb.ListResourceAgentsRequest) (*haifypb.ListResourceAgentsResponse, error) {
	return &haifypb.ListResourceAgentsResponse{Agents: []*haifypb.ResourceAgentInfo{{Name: "agent1"}}}, nil
}

func (m *mockServer) GetResourceAgentMetadata(ctx context.Context, req *haifypb.GetResourceAgentMetadataRequest) (*haifypb.GetResourceAgentMetadataResponse, error) {
	return &haifypb.GetResourceAgentMetadataResponse{}, nil
}

func (m *mockServer) GetHaToml(ctx context.Context, req *haifypb.GetHaTomlRequest) (*haifypb.GetHaTomlResponse, error) {
	return &haifypb.GetHaTomlResponse{Path: "/etc/ha.toml", Content: "toml"}, nil
}

func (m *mockServer) SyncHaToml(ctx context.Context, req *haifypb.SyncHaTomlRequest) (*haifypb.SyncHaTomlResponse, error) {
	return &haifypb.SyncHaTomlResponse{Success: true, Message: "synced"}, nil
}

func (m *mockServer) CreateSnapshotSchedule(ctx context.Context, req *haifypb.CreateSnapshotScheduleRequest) (*haifypb.CreateSnapshotScheduleResponse, error) {
	return &haifypb.CreateSnapshotScheduleResponse{Success: true}, nil
}

func (m *mockServer) ListSnapshotSchedules(ctx context.Context, req *haifypb.ListSnapshotSchedulesRequest) (*haifypb.ListSnapshotSchedulesResponse, error) {
	return &haifypb.ListSnapshotSchedulesResponse{Success: true}, nil
}

func (m *mockServer) DeleteSnapshotSchedule(ctx context.Context, req *haifypb.DeleteSnapshotScheduleRequest) (*haifypb.DeleteSnapshotScheduleResponse, error) {
	return &haifypb.DeleteSnapshotScheduleResponse{Success: true}, nil
}

func (m *mockServer) RestoreSnapshot(ctx context.Context, req *haifypb.RestoreSnapshotRequest) (*haifypb.RestoreSnapshotResponse, error) {
	return &haifypb.RestoreSnapshotResponse{Success: true}, nil
}

func (m *mockServer) CreateISCSIGateway(ctx context.Context, req *haifypb.CreateISCSIGatewayRequest) (*haifypb.CreateISCSIGatewayResponse, error) {
	return &haifypb.CreateISCSIGatewayResponse{Success: true}, nil
}

func (m *mockServer) CreateNVMeGateway(ctx context.Context, req *haifypb.CreateNVMeGatewayRequest) (*haifypb.CreateNVMeGatewayResponse, error) {
	return &haifypb.CreateNVMeGatewayResponse{Success: true}, nil
}

func (m *mockServer) GetGateway(ctx context.Context, req *haifypb.GetGatewayRequest) (*haifypb.GetGatewayResponse, error) {
	return &haifypb.GetGatewayResponse{Success: true, Gateway: &haifypb.GatewayInfo{Id: req.Id}}, nil
}

func (m *mockServer) StartGateway(ctx context.Context, req *haifypb.StartGatewayRequest) (*haifypb.StartGatewayResponse, error) {
	return &haifypb.StartGatewayResponse{Success: true}, nil
}

func (m *mockServer) StopGateway(ctx context.Context, req *haifypb.StopGatewayRequest) (*haifypb.StopGatewayResponse, error) {
	return &haifypb.StopGatewayResponse{Success: true}, nil
}

func (m *mockServer) AddNFSExport(ctx context.Context, req *haifypb.AddNFSExportRequest) (*haifypb.AddNFSExportResponse, error) {
	return &haifypb.AddNFSExportResponse{Success: true}, nil
}

func (m *mockServer) RemoveNFSExport(ctx context.Context, req *haifypb.RemoveNFSExportRequest) (*haifypb.RemoveNFSExportResponse, error) {
	return &haifypb.RemoveNFSExportResponse{Success: true}, nil
}

func (m *mockServer) ListNFSExports(ctx context.Context, req *haifypb.ListNFSExportsRequest) (*haifypb.ListNFSExportsResponse, error) {
	return &haifypb.ListNFSExportsResponse{Success: true}, nil
}

func (m *mockServer) AddISCSILUN(ctx context.Context, req *haifypb.AddISCSILUNRequest) (*haifypb.AddISCSILUNResponse, error) {
	return &haifypb.AddISCSILUNResponse{Success: true}, nil
}

func (m *mockServer) RemoveISCSILUN(ctx context.Context, req *haifypb.RemoveISCSILUNRequest) (*haifypb.RemoveISCSILUNResponse, error) {
	return &haifypb.RemoveISCSILUNResponse{Success: true}, nil
}

func (m *mockServer) ListISCSILUNs(ctx context.Context, req *haifypb.ListISCSILUNsRequest) (*haifypb.ListISCSILUNsResponse, error) {
	return &haifypb.ListISCSILUNsResponse{Success: true}, nil
}

func (m *mockServer) AddISCSIInitiator(ctx context.Context, req *haifypb.AddISCSIInitiatorRequest) (*haifypb.AddISCSIInitiatorResponse, error) {
	return &haifypb.AddISCSIInitiatorResponse{Success: true}, nil
}

func (m *mockServer) RemoveISCSIInitiator(ctx context.Context, req *haifypb.RemoveISCSIInitiatorRequest) (*haifypb.RemoveISCSIInitiatorResponse, error) {
	return &haifypb.RemoveISCSIInitiatorResponse{Success: true}, nil
}

func (m *mockServer) ListISCSIInitiators(ctx context.Context, req *haifypb.ListISCSIInitiatorsRequest) (*haifypb.ListISCSIInitiatorsResponse, error) {
	return &haifypb.ListISCSIInitiatorsResponse{Success: true}, nil
}

func (m *mockServer) SetISCSIChap(ctx context.Context, req *haifypb.SetISCSIChapRequest) (*haifypb.SetISCSIChapResponse, error) {
	return &haifypb.SetISCSIChapResponse{Success: true}, nil
}

func (m *mockServer) GetISCSIChap(ctx context.Context, req *haifypb.GetISCSIChapRequest) (*haifypb.GetISCSIChapResponse, error) {
	return &haifypb.GetISCSIChapResponse{Success: true}, nil
}

func (m *mockServer) AddNVMeNamespace(ctx context.Context, req *haifypb.AddNVMeNamespaceRequest) (*haifypb.AddNVMeNamespaceResponse, error) {
	return &haifypb.AddNVMeNamespaceResponse{Success: true}, nil
}

func (m *mockServer) RemoveNVMeNamespace(ctx context.Context, req *haifypb.RemoveNVMeNamespaceRequest) (*haifypb.RemoveNVMeNamespaceResponse, error) {
	return &haifypb.RemoveNVMeNamespaceResponse{Success: true}, nil
}

func (m *mockServer) ListNVMeNamespaces(ctx context.Context, req *haifypb.ListNVMeNamespacesRequest) (*haifypb.ListNVMeNamespacesResponse, error) {
	return &haifypb.ListNVMeNamespacesResponse{Success: true}, nil
}

func (m *mockServer) AddNVMeHost(ctx context.Context, req *haifypb.AddNVMeHostRequest) (*haifypb.AddNVMeHostResponse, error) {
	return &haifypb.AddNVMeHostResponse{Success: true}, nil
}

func (m *mockServer) RemoveNVMeHost(ctx context.Context, req *haifypb.RemoveNVMeHostRequest) (*haifypb.RemoveNVMeHostResponse, error) {
	return &haifypb.RemoveNVMeHostResponse{Success: true}, nil
}

func (m *mockServer) ListNVMeHosts(ctx context.Context, req *haifypb.ListNVMeHostsRequest) (*haifypb.ListNVMeHostsResponse, error) {
	return &haifypb.ListNVMeHostsResponse{Success: true}, nil
}

func (m *mockServer) CreateZFSPool(ctx context.Context, req *haifypb.CreateZFSPoolRequest) (*haifypb.CreateZFSPoolResponse, error) {
	return &haifypb.CreateZFSPoolResponse{Success: true}, nil
}

func (m *mockServer) DeleteZFSPool(ctx context.Context, req *haifypb.DeleteZFSPoolRequest) (*haifypb.DeleteZFSPoolResponse, error) {
	return &haifypb.DeleteZFSPoolResponse{Success: true}, nil
}

func (m *mockServer) ListZFSpools(ctx context.Context, req *haifypb.ListZFSPoolsRequest) (*haifypb.ListZFSPoolsResponse, error) {
	return &haifypb.ListZFSPoolsResponse{Success: true}, nil
}

func (m *mockServer) CreateZFSDataset(ctx context.Context, req *haifypb.CreateZFSDatasetRequest) (*haifypb.CreateZFSDatasetResponse, error) {
	return &haifypb.CreateZFSDatasetResponse{Success: true}, nil
}

func (m *mockServer) DeleteZFSDataset(ctx context.Context, req *haifypb.DeleteZFSDatasetRequest) (*haifypb.DeleteZFSDatasetResponse, error) {
	return &haifypb.DeleteZFSDatasetResponse{Success: true}, nil
}

func (m *mockServer) CreateZFSVolume(ctx context.Context, req *haifypb.CreateZFSVolumeRequest) (*haifypb.CreateZFSVolumeResponse, error) {
	return &haifypb.CreateZFSVolumeResponse{Success: true}, nil
}

func (m *mockServer) ResizeZFSVolume(ctx context.Context, req *haifypb.ResizeZFSVolumeRequest) (*haifypb.ResizeZFSVolumeResponse, error) {
	return &haifypb.ResizeZFSVolumeResponse{Success: true}, nil
}

func (m *mockServer) CreateZFSSnapshot(ctx context.Context, req *haifypb.CreateZFSSnapshotRequest) (*haifypb.CreateZFSSnapshotResponse, error) {
	return &haifypb.CreateZFSSnapshotResponse{Success: true}, nil
}

func (m *mockServer) DeleteZFSSnapshot(ctx context.Context, req *haifypb.DeleteZFSSnapshotRequest) (*haifypb.DeleteZFSSnapshotResponse, error) {
	return &haifypb.DeleteZFSSnapshotResponse{Success: true}, nil
}

func (m *mockServer) ListZFSSnapshots(ctx context.Context, req *haifypb.ListZFSSnapshotsRequest) (*haifypb.ListZFSSnapshotsResponse, error) {
	return &haifypb.ListZFSSnapshotsResponse{Success: true}, nil
}

func (m *mockServer) RestoreZFSSnapshot(ctx context.Context, req *haifypb.RestoreZFSSnapshotRequest) (*haifypb.RestoreZFSSnapshotResponse, error) {
	return &haifypb.RestoreZFSSnapshotResponse{Success: true}, nil
}

func (m *mockServer) CloneZFSSnapshot(ctx context.Context, req *haifypb.CloneZFSSnapshotRequest) (*haifypb.CloneZFSSnapshotResponse, error) {
	return &haifypb.CloneZFSSnapshotResponse{Success: true}, nil
}

func (m *mockServer) CreateLvmSnapshot(ctx context.Context, req *haifypb.CreateLvmSnapshotRequest) (*haifypb.CreateLvmSnapshotResponse, error) {
	return &haifypb.CreateLvmSnapshotResponse{Success: true}, nil
}

func (m *mockServer) DeleteLvmSnapshot(ctx context.Context, req *haifypb.DeleteLvmSnapshotRequest) (*haifypb.DeleteLvmSnapshotResponse, error) {
	return &haifypb.DeleteLvmSnapshotResponse{Success: true}, nil
}

func (m *mockServer) ListLvmSnapshots(ctx context.Context, req *haifypb.ListLvmSnapshotsRequest) (*haifypb.ListLvmSnapshotsResponse, error) {
	return &haifypb.ListLvmSnapshotsResponse{Success: true}, nil
}

func (m *mockServer) RestoreLvmSnapshot(ctx context.Context, req *haifypb.RestoreLvmSnapshotRequest) (*haifypb.RestoreLvmSnapshotResponse, error) {
	return &haifypb.RestoreLvmSnapshotResponse{Success: true}, nil
}
