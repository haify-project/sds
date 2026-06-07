package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
)

// mockClient embeds ControllerClient so only the methods a test exercises
// need implementations; calling anything else panics, which is a test bug.
type mockClient struct {
	ControllerClient

	listNodesFn      func(ctx context.Context) ([]*sdspb.NodeInfo, error)
	listPoolsFn      func(ctx context.Context) ([]*sdspb.PoolInfo, error)
	createPoolCalls  []string
	deleteResourceFn func(ctx context.Context, name string) error
	healthCheckFn    func(ctx context.Context, node string) (*client.NodeHealthInfo, error)
	lvmSnapshots     []string
}

func (m *mockClient) ListNodes(ctx context.Context) ([]*sdspb.NodeInfo, error) {
	return m.listNodesFn(ctx)
}

func (m *mockClient) ListPools(ctx context.Context) ([]*sdspb.PoolInfo, error) {
	return m.listPoolsFn(ctx)
}

func (m *mockClient) CreatePool(_ context.Context, name, poolType, node string, _ []string, _ uint64) error {
	m.createPoolCalls = append(m.createPoolCalls, name+"/"+poolType+"@"+node)
	return nil
}

func (m *mockClient) DeleteResource(ctx context.Context, name string) error {
	return m.deleteResourceFn(ctx, name)
}

func (m *mockClient) HealthCheck(ctx context.Context, node string) (*client.NodeHealthInfo, error) {
	return m.healthCheckFn(ctx, node)
}

func (m *mockClient) CreateLvmSnapshot(_ context.Context, pool, lvName, snapshotName, node, size string) error {
	m.lvmSnapshots = append(m.lvmSnapshots, strings.Join([]string{pool, lvName, snapshotName, node, size}, "|"))
	return nil
}

// connect spins up the MCP server against an in-memory transport and
// returns a connected client session.
func connect(t *testing.T, mock ControllerClient, readOnly bool) *mcp.ClientSession {
	t.Helper()
	srv := New(mock, zap.NewNop(), Options{ReadOnly: readOnly, Version: "test"}).MCPServer()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := cli.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func listTools(t *testing.T, session *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	tools := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

func TestToolRegistration(t *testing.T) {
	session := connect(t, &mockClient{}, false)
	tools := listTools(t, session)

	if len(tools) == 0 {
		t.Fatal("no tools registered")
	}
	// Every tool follows the sds_ naming convention and carries annotations.
	for name, tool := range tools {
		if !strings.HasPrefix(name, "sds_") {
			t.Errorf("tool %q does not use the sds_ prefix", name)
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description", name)
		}
		if tool.Annotations == nil || tool.Annotations.Title == "" {
			t.Errorf("tool %q has no annotations/title", name)
		}
	}
	// Spot-check the full surface is present.
	for _, want := range []string{
		"sds_node_list", "sds_node_register", "sds_node_health_check",
		"sds_pool_list", "sds_pool_create", "sds_pool_delete",
		"sds_resource_list", "sds_resource_create", "sds_resource_status",
		"sds_snapshot_create", "sds_snapshot_restore",
		"sds_gateway_list", "sds_gateway_create_nfs", "sds_gateway_create_iscsi", "sds_gateway_create_nvme",
		"sds_nfs_exports", "sds_iscsi_luns", "sds_iscsi_chap", "sds_nvme_namespaces",
		"sds_ha_create", "sds_ha_evict", "sds_self_ha_status", "sds_self_ha_enable",
	} {
		if _, found := tools[want]; !found {
			t.Errorf("expected tool %q not registered", want)
		}
	}
}

func TestDestructiveAnnotations(t *testing.T) {
	session := connect(t, &mockClient{}, false)
	tools := listTools(t, session)

	destructive := []string{
		"sds_resource_delete", "sds_pool_delete", "sds_snapshot_delete",
		"sds_snapshot_restore", "sds_gateway_delete", "sds_ha_delete", "sds_ha_evict",
	}
	for _, name := range destructive {
		tool, found := tools[name]
		if !found {
			t.Errorf("tool %q not registered", name)
			continue
		}
		ann := tool.Annotations
		if ann.DestructiveHint == nil || !*ann.DestructiveHint {
			t.Errorf("tool %q must carry DestructiveHint=true", name)
		}
		if ann.ReadOnlyHint {
			t.Errorf("tool %q must not be read-only", name)
		}
	}
	for _, name := range []string{"sds_node_list", "sds_pool_list", "sds_resource_status"} {
		if !tools[name].Annotations.ReadOnlyHint {
			t.Errorf("tool %q must carry ReadOnlyHint=true", name)
		}
	}
}

func TestReadOnlyMode(t *testing.T) {
	session := connect(t, &mockClient{}, true)
	tools := listTools(t, session)

	if len(tools) == 0 {
		t.Fatal("read-only mode registered no tools")
	}
	for name, tool := range tools {
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("read-only mode registered mutating tool %q", name)
		}
	}
	if _, found := tools["sds_resource_delete"]; found {
		t.Error("read-only mode must not register sds_resource_delete")
	}
	if _, found := tools["sds_node_list"]; !found {
		t.Error("read-only mode must register sds_node_list")
	}
}

func TestNodeListCall(t *testing.T) {
	mock := &mockClient{
		listNodesFn: func(context.Context) ([]*sdspb.NodeInfo, error) {
			return []*sdspb.NodeInfo{
				{Name: "orange1", Address: "192.168.123.214", State: "online", Version: "1.6.0"},
				{Name: "orange2", Address: "192.168.123.215", State: "online", Version: "1.6.0"},
			}, nil
		},
	}
	session := connect(t, mock, false)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_node_list"})
	if err != nil {
		t.Fatalf("call sds_node_list: %v", err)
	}
	if res.IsError {
		t.Fatalf("sds_node_list returned tool error: %v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out nodeListOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	if len(out.Nodes) != 2 || out.Nodes[0].Name != "orange1" || out.Nodes[1].State != "online" {
		t.Fatalf("unexpected node list output: %+v", out)
	}
}

func TestPoolCreateTypeDispatch(t *testing.T) {
	mock := &mockClient{}
	session := connect(t, mock, false)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_pool_create",
		Arguments: map[string]any{
			"name":    "data-pool",
			"type":    "lvm-thin",
			"nodes":   []string{"orange1", "orange2"},
			"devices": []string{"/dev/sdb"},
			"size_gb": 10,
		},
	})
	if err != nil {
		t.Fatalf("call sds_pool_create: %v", err)
	}
	if res.IsError {
		t.Fatalf("sds_pool_create returned tool error: %v", res.Content)
	}
	// lvm-thin must be normalized to the backend type thin_pool on every node.
	want := []string{"data-pool/thin_pool@orange1", "data-pool/thin_pool@orange2"}
	if len(mock.createPoolCalls) != 2 || mock.createPoolCalls[0] != want[0] || mock.createPoolCalls[1] != want[1] {
		t.Fatalf("unexpected CreatePool calls: %v", mock.createPoolCalls)
	}
}

func TestSnapshotNamingConvention(t *testing.T) {
	mock := &mockClient{}
	session := connect(t, mock, false)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "sds_snapshot_create",
		Arguments: map[string]any{
			"resource": "data",
			"name":     "snap1",
			"node":     "orange1",
			"pool":     "sds_vg0",
		},
	})
	if err != nil {
		t.Fatalf("call sds_snapshot_create: %v", err)
	}
	if res.IsError {
		t.Fatalf("sds_snapshot_create returned tool error: %v", res.Content)
	}
	// LVM snapshots target LV "<resource>_data" with a default 1G COW size.
	if len(mock.lvmSnapshots) != 1 || mock.lvmSnapshots[0] != "sds_vg0|data_data|snap1|orange1|1G" {
		t.Fatalf("unexpected snapshot call: %v", mock.lvmSnapshots)
	}
}

func TestToolErrorPropagation(t *testing.T) {
	mock := &mockClient{
		deleteResourceFn: func(_ context.Context, name string) error {
			return context.DeadlineExceeded
		},
	}
	session := connect(t, mock, false)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_resource_delete",
		Arguments: map[string]any{"name": "data"},
	})
	if err != nil {
		t.Fatalf("protocol error instead of tool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true when the controller call fails")
	}
}

func TestInvalidInputRejected(t *testing.T) {
	session := connect(t, &mockClient{}, false)

	// Missing required field "name" must be rejected by schema validation
	// before the handler runs.
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "sds_resource_delete",
		Arguments: map[string]any{},
	})
	if err == nil && !res.IsError {
		t.Fatal("expected missing required argument to be rejected")
	}
}
