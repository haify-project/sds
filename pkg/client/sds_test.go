package client

import (
	"context"
	"net"
	"testing"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
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

	conn, err := grpc.DialContext(
		context.Background(),
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

func TestSDSClientNodeOperations(t *testing.T) {
	c, cleanup := setupMockClient(t)
	defer cleanup()

	ctx := context.Background()

	// RegisterNode
	node, err := c.RegisterNode(ctx, "test-node", "10.0.0.3")
	require.NoError(t, err)
	assert.Equal(t, "test-node", node.Name)

	_, err = c.RegisterNode(ctx, "error-node", "10.0.0.4")
	assert.Error(t, err)

	// UnregisterNode
	err = c.UnregisterNode(ctx, "10.0.0.3")
	require.NoError(t, err)

	err = c.UnregisterNode(ctx, "fail-addr")
	assert.Error(t, err)

	// ListNodes & GetNode & SetNodeLabels
	nodes, err := c.ListNodes(ctx)
	require.NoError(t, err)
	assert.Len(t, nodes, 2)

	gnode, err := c.GetNode(ctx, "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "node1", gnode.Name)

	_, err = c.GetNode(ctx, "fail-node")
	assert.Error(t, err)

	lnode, err := c.SetNodeLabels(ctx, "node1", map[string]string{"env": "test"}, false)
	require.NoError(t, err)
	assert.Equal(t, "test", lnode.Labels["env"])

	_, err = c.SetNodeLabels(ctx, "fail-node", nil, false)
	assert.Error(t, err)

	// HealthCheck
	h, err := c.HealthCheck(ctx, "node1")
	require.NoError(t, err)
	assert.True(t, h.DrbdInstalled)

	_, err = c.HealthCheck(ctx, "fail-node")
	assert.Error(t, err)

	// DrainNode / UndrainNode
	moved, err := c.DrainNode(ctx, "node1")
	require.NoError(t, err)
	assert.Equal(t, []string{"res1"}, moved)

	_, err = c.DrainNode(ctx, "fail-node")
	assert.Error(t, err)

	err = c.UndrainNode(ctx, "node1")
	require.NoError(t, err)

	err = c.UndrainNode(ctx, "fail-node")
	assert.Error(t, err)
}

func TestSDSClientPoolAndResourceOperations(t *testing.T) {
	c, cleanup := setupMockClient(t)
	defer cleanup()

	ctx := context.Background()

	// Pool Operations
	err := c.CreatePool(ctx, "pool1", "lvm", "node1", []string{"/dev/sdb"}, 100)
	require.NoError(t, err)

	err = c.CreatePool(ctx, "fail-pool", "lvm", "node1", nil, 0)
	assert.Error(t, err)

	pools, err := c.ListPools(ctx)
	require.NoError(t, err)
	assert.Len(t, pools, 1)

	pool, err := c.GetPool(ctx, "pool1", "node1")
	require.NoError(t, err)
	assert.Equal(t, "pool1", pool.Name)

	_, err = c.GetPool(ctx, "fail-pool", "node1")
	assert.Error(t, err)

	err = c.AddDiskToPool(ctx, "pool1", "/dev/sdc", "node1")
	require.NoError(t, err)

	err = c.AddDiskToPool(ctx, "fail-pool", "/dev/sdc", "node1")
	assert.Error(t, err)

	err = c.DeletePool(ctx, "pool1", "node1")
	require.NoError(t, err)

	err = c.DeletePool(ctx, "fail-pool", "node1")
	assert.Error(t, err)

	// Resource Operations
	err = c.CreateResourceWithPoolAndType(ctx, "res1", 7001, []string{"node1"}, "C", 10, "pool1", "lvm", nil)
	require.NoError(t, err)

	err = c.CreateResourceWithPoolAndType(ctx, "fail-res", 7001, []string{"node1"}, "C", 10, "pool1", "lvm", nil)
	assert.Error(t, err)

	rList, err := c.ListResources(ctx)
	require.NoError(t, err)
	assert.Len(t, rList, 1)

	rInfo, err := c.GetResource(ctx, "res1")
	require.NoError(t, err)
	assert.Equal(t, "res1", rInfo.Name)

	_, err = c.GetResource(ctx, "fail-res")
	assert.Error(t, err)

	err = c.SetPrimary(ctx, "res1", "node1", false)
	require.NoError(t, err)

	err = c.SetPrimary(ctx, "fail-res", "node1", false)
	assert.Error(t, err)

	err = c.SetSecondary(ctx, "res1", "node1")
	require.NoError(t, err)

	err = c.SetSecondary(ctx, "fail-res", "node1")
	assert.Error(t, err)

	err = c.AttachDisklessClient(ctx, "res1", "node2")
	require.NoError(t, err)

	err = c.AttachDisklessClient(ctx, "fail-res", "node2")
	assert.Error(t, err)

	err = c.DetachDisklessClient(ctx, "res1", "node2")
	require.NoError(t, err)

	err = c.DetachDisklessClient(ctx, "fail-res", "node2")
	assert.Error(t, err)

	err = c.ResizeVolume(ctx, "res1", 0, 20)
	require.NoError(t, err)

	err = c.ResizeVolume(ctx, "fail-res", 0, 20)
	assert.Error(t, err)

	err = c.DeleteResource(ctx, "res1")
	require.NoError(t, err)

	err = c.DeleteResource(ctx, "fail-res")
	assert.Error(t, err)
}

func TestSDSClientHaSnapshotGatewayOperations(t *testing.T) {
	c, cleanup := setupMockClient(t)
	defer cleanup()

	ctx := context.Background()

	// HA
	configPath, err := c.MakeHa(ctx, "res1", []string{"svc1"}, "/mnt", "ext4", "10.0.0.100", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "/etc/ha", configPath)

	_, err = c.MakeHa(ctx, "fail-res", nil, "", "", "", nil, nil)
	assert.Error(t, err)

	haInfo, err := c.GetHa(ctx, "res1")
	require.NoError(t, err)
	assert.Equal(t, "res1", haInfo.Resource)

	_, err = c.GetHa(ctx, "fail-res")
	assert.Error(t, err)

	haList, err := c.ListHa(ctx)
	require.NoError(t, err)
	assert.Len(t, haList, 1)

	promoters, err := c.GetHaStatus(ctx, "res1")
	require.NoError(t, err)
	assert.Len(t, promoters, 1)

	// Snapshot
	err = c.CreateSnapshot(ctx, "vol1", "snap1", "node1")
	require.NoError(t, err)

	err = c.CreateSnapshot(ctx, "vol1", "fail-snap", "node1")
	assert.Error(t, err)

	snaps, err := c.ListSnapshots(ctx, "vol1", "node1")
	require.NoError(t, err)
	assert.Len(t, snaps, 1)

	err = c.DeleteSnapshot(ctx, "vol1", "snap1", "node1")
	require.NoError(t, err)

	err = c.DeleteSnapshot(ctx, "vol1", "fail-snap", "node1")
	assert.Error(t, err)

	// Gateway
	gwReq := &sdspb.CreateNFSGatewayRequest{
		Resource:   "res1",
		ServiceIp:  "10.0.0.100/24",
		ExportPath: "/export",
	}
	gw, err := c.CreateNFSGateway(ctx, gwReq)
	require.NoError(t, err)
	assert.True(t, gw.Success)

	gwFailReq := &sdspb.CreateNFSGatewayRequest{Resource: "fail-res"}
	_, err = c.CreateNFSGateway(ctx, gwFailReq)
	assert.Error(t, err)

	gwList, err := c.ListGateways(ctx)
	require.NoError(t, err)
	assert.Len(t, gwList, 1)

	err = c.DeleteGateway(ctx, "gw1")
	require.NoError(t, err)

	err = c.DeleteGateway(ctx, "fail-gw")
	assert.Error(t, err)
}
