package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/client"
)

const dbAppSecret = "s3cret-generated-password-xyz"

type dbAppClient struct {
	mockClient
	created client.AppCreateRequest
}

func dbAppInfo(name string) *haifypb.AppInfo {
	return &haifypb.AppInfo{Name: name, Engine: "postgres", Resource: name, ServiceIp: "10.0.0.50/24", Port: 5432,
		AdminUser: "postgres", CredentialsFile: "/var/lib/haify-app/" + name + "/haify/password",
		Connection: "postgresql://postgres@10.0.0.50:5432/postgres"}
}

func (m *dbAppClient) CreateApp(_ context.Context, req client.AppCreateRequest) (*haifypb.CreateAppResponse, error) {
	m.created = req
	return &haifypb.CreateAppResponse{Success: true, App: dbAppInfo(req.Name), Password: dbAppSecret}, nil
}

func (m *dbAppClient) ListApps(context.Context) ([]*haifypb.AppInfo, error) {
	return []*haifypb.AppInfo{dbAppInfo("orders")}, nil
}

func (m *dbAppClient) GetAppStatus(_ context.Context, name string) (*haifypb.GetAppStatusResponse, error) {
	return &haifypb.GetAppStatusResponse{Success: true, App: dbAppInfo(name), State: "running", PrimaryNode: "node1",
		ServiceState: "active", Healthy: true, Nodes: []string{"node1", "node2"}}, nil
}

func resultJSON(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	b, err := json.Marshal(res)
	require.NoError(t, err)
	return string(b)
}

func TestDBAppTools(t *testing.T) {
	mock := &dbAppClient{}
	session := connect(t, mock, false)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "haify_app_create", Arguments: map[string]any{
		"name": "orders", "engine": "postgres", "service_ip": "10.0.0.50/24", "vector": true}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	assert.Equal(t, client.AppCreateRequest{Name: "orders", Engine: "postgres", ServiceIP: "10.0.0.50/24", Vector: true},
		mock.created)
	out := resultJSON(t, res)
	assert.NotContains(t, out, dbAppSecret, "a tool result is recorded by its caller; the password stays on the volume")
	assert.Contains(t, out, "/var/lib/haify-app/orders/haify/password")

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "haify_app_status",
		Arguments: map[string]any{"name": "orders"}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	assert.Contains(t, resultJSON(t, res), `"state":"running"`)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "haify_app_list"})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	assert.Contains(t, resultJSON(t, res), `"name":"orders"`)
}

func TestDBAppToolTiers(t *testing.T) {
	read := toolNames(t, New(&dbAppClient{}, zap.NewNop(), Options{ReadOnly: true}))
	assert.True(t, read["haify_app_list"])
	assert.True(t, read["haify_app_status"])
	assert.False(t, read["haify_app_create"])

	operate := toolNames(t, New(&dbAppClient{}, zap.NewNop(), Options{NoDestructive: true}))
	assert.True(t, operate["haify_app_create"])
	assert.True(t, operate["haify_app_snapshot"])
	assert.False(t, operate["haify_app_failover"], "a failover interrupts service")
	assert.False(t, operate["haify_app_delete"])

	all := toolNames(t, New(&dbAppClient{}, zap.NewNop(), Options{}))
	for _, name := range []string{"haify_app_list", "haify_app_status", "haify_app_create", "haify_app_snapshot",
		"haify_app_failover", "haify_app_delete"} {
		assert.True(t, all[name], name)
	}
}
