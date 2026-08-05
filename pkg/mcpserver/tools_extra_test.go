package mcpserver

import (
	"context"
	"testing"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockExtraClient struct {
	mockClient
	gateways  []*sdspb.GatewayInfo
	haConfigs []*sdspb.HaConfigInfo
}

func (m *mockExtraClient) ListGateways(ctx context.Context) ([]*sdspb.GatewayInfo, error) {
	return m.gateways, nil
}

func (m *mockExtraClient) ListHa(ctx context.Context) ([]*sdspb.HaConfigInfo, error) {
	return m.haConfigs, nil
}

func (m *mockExtraClient) MakeHa(_ context.Context, resource string, _ []string, _, _, _ string, _ []*sdspb.OcfAgent, _ []*sdspb.HaStartItem) (string, error) {
	return "/etc/ha", nil
}

func (m *mockExtraClient) EvictHa(_ context.Context, resource string) error {
	return nil
}

func (m *mockExtraClient) CreateLvmSnapshot(_ context.Context, _, _, _, _, _ string) error {
	return nil
}

func (m *mockExtraClient) DeleteLvmSnapshot(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *mockExtraClient) RestoreLvmSnapshot(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockExtraClient) CreateNFSGateway(_ context.Context, req *sdspb.CreateNFSGatewayRequest) (*sdspb.CreateNFSGatewayResponse, error) {
	return &sdspb.CreateNFSGatewayResponse{Success: true, ConfigPath: "/etc/exports.d/nfs.conf"}, nil
}

func (m *mockExtraClient) DeleteGateway(_ context.Context, id string) error {
	return nil
}

func (m *mockExtraClient) CreateISCSIGateway(_ context.Context, req *sdspb.CreateISCSIGatewayRequest) (*sdspb.CreateISCSIGatewayResponse, error) {
	return &sdspb.CreateISCSIGatewayResponse{Success: true}, nil
}

func (m *mockExtraClient) CreateNVMeGateway(_ context.Context, req *sdspb.CreateNVMeGatewayRequest) (*sdspb.CreateNVMeGatewayResponse, error) {
	return &sdspb.CreateNVMeGatewayResponse{Success: true}, nil
}

func TestMCPExtraTools(t *testing.T) {
	mc := &mockExtraClient{
		mockClient: mockClient{
			listNodesFn: func(ctx context.Context) ([]*sdspb.NodeInfo, error) {
				return []*sdspb.NodeInfo{{Name: "n1", Address: "10.0.0.1"}}, nil
			},
			listPoolsFn: func(ctx context.Context) ([]*sdspb.PoolInfo, error) {
				return []*sdspb.PoolInfo{{Name: "vg0", Type: "lvm", Node: "n1", FreeGb: 50}}, nil
			},
		},
		gateways: []*sdspb.GatewayInfo{
			{Id: "gw1", Resource: "nfs-gw", Type: "nfs"},
		},
		haConfigs: []*sdspb.HaConfigInfo{
			{Resource: "res1", Vip: "10.0.0.1"},
		},
	}

	session := connect(t, mc, false)

	// Call sds_node_list
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_node_list"})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_pool_list
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_pool_list"})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_gateway_list
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_gateway_list"})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_ha_list
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_ha_list"})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_ha_create
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_ha_create",
		Arguments: map[string]interface{}{
			"resource": "res1",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_ha_evict
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_ha_evict",
		Arguments: map[string]interface{}{
			"resource": "res1",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_snapshot_create
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_snapshot_create",
		Arguments: map[string]interface{}{
			"resource": "res1",
			"name":     "snap1",
			"node":     "n1",
			"pool":     "vg0",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_snapshot_delete
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_snapshot_delete",
		Arguments: map[string]interface{}{
			"resource": "res1",
			"name":     "snap1",
			"node":     "n1",
			"pool":     "vg0",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_gateway_create_nfs
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_gateway_create_nfs",
		Arguments: map[string]interface{}{
			"resource":    "res1",
			"service_ip":  "10.0.0.100/24",
			"export_path": "/export",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_gateway_create_iscsi
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_gateway_create_iscsi",
		Arguments: map[string]interface{}{
			"resource":   "res1",
			"service_ip": "10.0.0.100/24",
			"iqn":        "iqn.2024.com.example:target",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_gateway_create_nvme
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_gateway_create_nvme",
		Arguments: map[string]interface{}{
			"resource":   "res1",
			"service_ip": "10.0.0.100/24",
			"nqn":        "nqn.2024.com.example:target",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// Call sds_gateway_delete
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_gateway_delete",
		Arguments: map[string]interface{}{
			"resource": "res1",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)
}

func (m *mockExtraClient) GetSelfHaStatus(_ context.Context) (*client.SelfHaStatus, error) {
	return &client.SelfHaStatus{Enabled: true, Resource: "sds-meta", VIP: "10.0.0.250"}, nil
}

func (m *mockExtraClient) EnableSelfHa(_ context.Context, _, _ string, _, _ uint32, _ []string) (string, string, error) {
	return "/etc/ha", "script", nil
}

func (m *mockExtraClient) DisableSelfHa(_ context.Context, _ string) error {
	return nil
}

func (m *mockExtraClient) ListNFSExports(_ context.Context, _ string) ([]*sdspb.NFSExportInfo, error) {
	return []*sdspb.NFSExportInfo{{Directory: "/export"}}, nil
}

func (m *mockExtraClient) AddNFSExport(_ context.Context, _, _ string, _ int32, _, _ string) error {
	return nil
}

func (m *mockExtraClient) RemoveNFSExport(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockExtraClient) ListISCSILUNs(_ context.Context, _ string) ([]*sdspb.ISCSILUNInfo, error) {
	return []*sdspb.ISCSILUNInfo{{Lun: 1, Device: "/dev/drbd0"}}, nil
}

func (m *mockExtraClient) AddISCSILUN(_ context.Context, _ string, _ int32, _ string) error {
	return nil
}

func (m *mockExtraClient) RemoveISCSILUN(_ context.Context, _ string, _ int32) error {
	return nil
}

func (m *mockExtraClient) ListNVMeNamespaces(_ context.Context, _ string) ([]*sdspb.NVMeNamespaceInfo, error) {
	return []*sdspb.NVMeNamespaceInfo{{NamespaceId: 1, BackingPath: "/dev/drbd0"}}, nil
}

func (m *mockExtraClient) AddNVMeNamespace(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockExtraClient) RemoveNVMeNamespace(_ context.Context, _ string, _ int32) error {
	return nil
}

func TestMCPExtraSubTools(t *testing.T) {
	mc := &mockExtraClient{}
	session := connect(t, mc, false)

	// sds_self_ha_status
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_self_ha_status"})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_self_ha_enable
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_self_ha_enable",
		Arguments: map[string]interface{}{
			"vip":  "10.0.0.250/24",
			"pool": "vg0",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_self_ha_disable
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_self_ha_disable",
		Arguments: map[string]interface{}{
			"node": "n1",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_nfs_exports list, add, remove
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_nfs_exports",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "list"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_nfs_exports",
		Arguments: map[string]interface{}{
			"resource":    "gw1",
			"action":      "add",
			"export_path": "/export",
			"client_spec": "*",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_nfs_exports",
		Arguments: map[string]interface{}{
			"resource":    "gw1",
			"action":      "remove",
			"export_path": "/export",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_iscsi_luns list, add, remove
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_iscsi_luns",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "list"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_iscsi_luns",
		Arguments: map[string]interface{}{
			"resource": "gw1",
			"action":   "add",
			"lun":      1,
			"device":   "/dev/drbd0",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_iscsi_luns",
		Arguments: map[string]interface{}{
			"resource": "gw1",
			"action":   "remove",
			"lun":      1,
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_nvme_namespaces list, add, remove
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_nvme_namespaces",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "list"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_nvme_namespaces",
		Arguments: map[string]interface{}{
			"resource": "gw1",
			"action":   "add",
			"device":   "/dev/drbd0",
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_nvme_namespaces",
		Arguments: map[string]interface{}{
			"resource":     "gw1",
			"action":       "remove",
			"namespace_id": 1,
		},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)
}

func (m *mockExtraClient) ListISCSIInitiators(_ context.Context, _ string) ([]string, error) {
	return []string{"iqn.init"}, nil
}
func (m *mockExtraClient) AddISCSIInitiator(_ context.Context, _, _ string) error    { return nil }
func (m *mockExtraClient) RemoveISCSIInitiator(_ context.Context, _, _ string) error { return nil }

func (m *mockExtraClient) GetISCSIChap(_ context.Context, _ string) (*sdspb.GetISCSIChapResponse, error) {
	return &sdspb.GetISCSIChapResponse{Success: true, Username: "user"}, nil
}
func (m *mockExtraClient) SetISCSIChap(_ context.Context, _, _, _ string, _ bool) error { return nil }

func (m *mockExtraClient) ListNVMeHosts(_ context.Context, _ string) ([]string, error) {
	return []string{"nqn.host"}, nil
}
func (m *mockExtraClient) AddNVMeHost(_ context.Context, _, _ string) error    { return nil }
func (m *mockExtraClient) RemoveNVMeHost(_ context.Context, _, _ string) error { return nil }

func (m *mockExtraClient) ListSnapshotSchedules(_ context.Context) ([]*sdspb.SnapshotScheduleInfo, error) {
	return []*sdspb.SnapshotScheduleInfo{{Resource: "res1", Cron: "0 * * * *"}}, nil
}
func (m *mockExtraClient) CreateSnapshotSchedule(_ context.Context, _, _ string, _ *sdspb.GFSRetention, _ bool) error {
	return nil
}
func (m *mockExtraClient) DeleteSnapshotSchedule(_ context.Context, _ string) error { return nil }
func (m *mockExtraClient) RestoreSnapshot(_ context.Context, _, _, _ string) error  { return nil }

func (m *mockExtraClient) UpdateResourceOptions(_ context.Context, _ string, _ map[string]string) error {
	return nil
}
func (m *mockExtraClient) SetPrimary(_ context.Context, _, _ string, _ bool) error { return nil }
func (m *mockExtraClient) SetSecondary(_ context.Context, _, _ string) error       { return nil }

func TestMCPExtraSubTools2(t *testing.T) {
	mc := &mockExtraClient{}
	session := connect(t, mc, false)

	// sds_iscsi_initiators list, add, remove
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_iscsi_initiators",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "list"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_iscsi_initiators",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "add", "iqn": "iqn.init"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_iscsi_initiators",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "remove", "iqn": "iqn.init"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_iscsi_chap get, set
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_iscsi_chap",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "get"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_iscsi_chap",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "set", "username": "user", "password": "pass"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_nvme_hosts list, add, remove
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_nvme_hosts",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "list"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_nvme_hosts",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "add", "host_nqn": "nqn.host"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_nvme_hosts",
		Arguments: map[string]interface{}{"resource": "gw1", "action": "remove", "host_nqn": "nqn.host"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_snapshot_schedule_list, create, delete
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_snapshot_schedule_list",
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_snapshot_schedule_create",
		Arguments: map[string]interface{}{"resource": "res1", "cron": "0 * * * *", "keep_daily": 7},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_snapshot_schedule_delete",
		Arguments: map[string]interface{}{"name": "res1"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_snapshot_restore
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_snapshot_restore",
		Arguments: map[string]interface{}{"resource": "res1", "name": "snap1", "node": "n1", "pool": "vg0"},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)

	// sds_resource_set_options
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_resource_set_options",
		Arguments: map[string]interface{}{"resource": "res1", "options": map[string]string{"opt": "val"}},
	})
	require.NoError(t, err)
	if res.IsError {
		t.Logf("tool error: %+v", res.Content[0])
	}
	assert.False(t, res.IsError)
}

func (m *mockExtraClient) RegisterNode(_ context.Context, name, address string) (*sdspb.NodeInfo, error) {
	return &sdspb.NodeInfo{Name: name, Address: address}, nil
}
func (m *mockExtraClient) HealthCheck(_ context.Context, node string) (*client.NodeHealthInfo, error) {
	return &client.NodeHealthInfo{DrbdInstalled: true}, nil
}
func (m *mockExtraClient) DeletePool(_ context.Context, _, _ string) error { return nil }

func (m *mockExtraClient) ListResources(_ context.Context) ([]*sdspb.ResourceInfo, error) {
	return []*sdspb.ResourceInfo{{Name: "res1", Role: "Primary"}}, nil
}
func (m *mockExtraClient) StartGateway(_ context.Context, _ string) error { return nil }
func (m *mockExtraClient) StopGateway(_ context.Context, _ string) error  { return nil }

func TestMCPExtraSubTools3(t *testing.T) {
	mc := &mockExtraClient{}
	session := connect(t, mc, false)

	// sds_node_register
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_node_register",
		Arguments: map[string]interface{}{"name": "n2", "address": "10.0.0.2"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_node_health_check
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_node_health_check",
		Arguments: map[string]interface{}{"nodes": []string{"n1"}},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_pool_delete
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_pool_delete",
		Arguments: map[string]interface{}{"name": "vg0", "node": "n1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_resource_list
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_resource_list",
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_gateway_start & stop
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_gateway_start",
		Arguments: map[string]interface{}{"resource": "gw1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_gateway_stop",
		Arguments: map[string]interface{}{"resource": "gw1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)
}

func (m *mockExtraClient) AddDiskToPool(_ context.Context, _, _, _ string) error { return nil }
func (m *mockExtraClient) ResourceStatus(_ context.Context, name string) (*sdspb.ResourceStatus, error) {
	return &sdspb.ResourceStatus{Name: name}, nil
}
func (m *mockExtraClient) AddVolume(_ context.Context, _, _, _ string, _ uint32) error { return nil }
func (m *mockExtraClient) RemoveVolume(_ context.Context, _ string, _ uint32) error    { return nil }
func (m *mockExtraClient) ResizeVolume(_ context.Context, _ string, _ uint32, _ uint32) error {
	return nil
}
func (m *mockExtraClient) CreateFilesystem(_ context.Context, _ string, _ uint32, _, _ string) error {
	return nil
}
func (m *mockExtraClient) MountResource(_ context.Context, _ string, _ uint32, _, _, _ string) error {
	return nil
}
func (m *mockExtraClient) UnmountResource(_ context.Context, _ string, _ uint32, _ string) error {
	return nil
}
func (m *mockExtraClient) DeleteHa(_ context.Context, _ string) error { return nil }
func (m *mockExtraClient) GetHa(_ context.Context, resource string) (*sdspb.HaConfigInfo, error) {
	return &sdspb.HaConfigInfo{Resource: resource}, nil
}
func (m *mockExtraClient) GetGateway(_ context.Context, id string) (*sdspb.GatewayInfo, error) {
	return &sdspb.GatewayInfo{Id: id}, nil
}
func (m *mockExtraClient) ListLvmSnapshots(_ context.Context, _, _ string) ([]*sdspb.SnapshotInfo, error) {
	return []*sdspb.SnapshotInfo{{Name: "snap1"}}, nil
}

func TestMCPExtraSubTools4(t *testing.T) {
	mc := &mockExtraClient{}
	session := connect(t, mc, false)

	// sds_node_unregister
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_node_unregister",
		Arguments: map[string]interface{}{"address": "10.0.0.2"},
	})
	require.NoError(t, err)

	assert.False(t, res.IsError)

	// sds_pool_add_disk
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_pool_add_disk",
		Arguments: map[string]interface{}{"pool": "vg0", "nodes": []string{"n1"}, "devices": []string{"/dev/sdc"}},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_resource_status
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_resource_status",
		Arguments: map[string]interface{}{"name": "res1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_resource_add_volume
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_resource_add_volume",
		Arguments: map[string]interface{}{"resource": "res1", "volume": "vol1", "size_gb": 10, "pool": "vg0"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_resource_resize_volume
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_resource_resize_volume",
		Arguments: map[string]interface{}{"resource": "res1", "volume_id": 0, "size_gb": 20},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_resource_create_filesystem
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_resource_create_filesystem",
		Arguments: map[string]interface{}{"resource": "res1", "volume_id": 0, "fstype": "ext4", "node": "n1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_resource_mount
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_resource_mount",
		Arguments: map[string]interface{}{"resource": "res1", "volume_id": 0, "path": "/mnt", "node": "n1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_resource_unmount
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_resource_unmount",
		Arguments: map[string]interface{}{"resource": "res1", "volume_id": 0, "node": "n1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_gateway_get
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_gateway_get",
		Arguments: map[string]interface{}{"resource": "gw1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_ha_status
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_ha_status",
		Arguments: map[string]interface{}{"resource": "res1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_ha_delete
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_ha_delete",
		Arguments: map[string]interface{}{"resource": "res1"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// sds_snapshot_list
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_snapshot_list",
		Arguments: map[string]interface{}{"resource": "res1", "node": "n1", "pool": "vg0"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)
}
