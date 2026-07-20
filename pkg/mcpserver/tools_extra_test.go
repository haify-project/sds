package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockExtraClient struct {
	mockClient
	gateways []*sdspb.GatewayInfo
	haConfigs []*sdspb.HaConfigInfo
}

func (m *mockExtraClient) ListGateways(ctx context.Context) ([]*sdspb.GatewayInfo, error) {
	return m.gateways, nil
}

func (m *mockExtraClient) ListHa(ctx context.Context) ([]*sdspb.HaConfigInfo, error) {
	return m.haConfigs, nil
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
	assert.False(t, res.IsError)

	// Call sds_pool_list
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_pool_list"})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// Call sds_gateway_list
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_gateway_list"})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// Call sds_ha_list
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sds_ha_list"})
	require.NoError(t, err)
	assert.False(t, res.IsError)
}
