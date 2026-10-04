package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/client"
)

const dbAppSecret = "s3cret-generated-password-xyz"

type dbAppClient struct {
	mockClient
	created client.AppCreateRequest
}

func dbAppInfo(name string) *sdspb.AppInfo {
	return &sdspb.AppInfo{Name: name, Engine: "postgres", Resource: name, ServiceIp: "10.0.0.50/24", Port: 5432,
		AdminUser: "postgres", CredentialsFile: "/var/lib/sds-app/" + name + "/sds/password",
		Connection: "postgresql://postgres@10.0.0.50:5432/postgres"}
}

func (m *dbAppClient) CreateApp(_ context.Context, req client.AppCreateRequest) (*sdspb.CreateAppResponse, error) {
	m.created = req
	return &sdspb.CreateAppResponse{Success: true, App: dbAppInfo(req.Name), Password: dbAppSecret}, nil
}

func (m *dbAppClient) ListApps(context.Context) ([]*sdspb.AppInfo, error) {
	return []*sdspb.AppInfo{dbAppInfo("orders")}, nil
}

func (m *dbAppClient) GetAppStatus(_ context.Context, name string) (*sdspb.GetAppStatusResponse, error) {
	return &sdspb.GetAppStatusResponse{Success: true, App: dbAppInfo(name), State: "running", PrimaryNode: "node1",
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

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_app_create", Arguments: map[string]any{
		"name": "orders", "engine": "postgres", "service_ip": "10.0.0.50/24", "vector": true}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	assert.Equal(t, client.AppCreateRequest{Name: "orders", Engine: "postgres", ServiceIP: "10.0.0.50/24", Vector: true},
		mock.created)
	out := resultJSON(t, res)
	assert.NotContains(t, out, dbAppSecret, "a tool result is recorded by its caller; the password stays on the volume")
	assert.Contains(t, out, "/var/lib/sds-app/orders/sds/password")

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_app_status",
		Arguments: map[string]any{"name": "orders"}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	assert.Contains(t, resultJSON(t, res), `"state":"running"`)

	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_app_list"})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	assert.Contains(t, resultJSON(t, res), `"name":"orders"`)
}

func TestDBAppToolTiers(t *testing.T) {
	read := toolNames(t, New(&dbAppClient{}, zap.NewNop(), Options{ReadOnly: true}))
	assert.True(t, read["sds_app_list"])
	assert.True(t, read["sds_app_status"])
	assert.False(t, read["sds_app_create"])

	operate := toolNames(t, New(&dbAppClient{}, zap.NewNop(), Options{NoDestructive: true}))
	assert.True(t, operate["sds_app_create"])
	assert.True(t, operate["sds_app_snapshot"])
	assert.False(t, operate["sds_app_failover"], "a failover interrupts service")
	assert.False(t, operate["sds_app_delete"])

	all := toolNames(t, New(&dbAppClient{}, zap.NewNop(), Options{}))
	for _, name := range []string{"sds_app_list", "sds_app_status", "sds_app_create", "sds_app_snapshot",
		"sds_app_failover", "sds_app_delete"} {
		assert.True(t, all[name], name)
	}
}
