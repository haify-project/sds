package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/gateway"
)

// listingGatewayDeployment answers the gateway manager's node listing with a
// fixed per-host output.
type listingGatewayDeployment struct {
	fakeGatewayDeployment
	outputs map[string]string
}

func (f *listingGatewayDeployment) ExecOutput(ctx context.Context, hosts []string, cmd string) (map[string]string, error) {
	out := map[string]string{}
	for _, h := range hosts {
		if v, ok := f.outputs[h]; ok {
			out[h] = v
		}
	}
	return out, nil
}

// The gateway list comes from the storage nodes, so it is right on a
// controller that is not itself a gateway node.
func TestServerListGatewaysReadsStorageNodes(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctrl.gateway = gateway.New(nil, &listingGatewayDeployment{outputs: map[string]string{
		"10.0.0.2": "haify-nfs-data.toml\nhaify-iscsi-blk.toml.disabled\n",
	}}, zap.NewNop(), []string{"10.0.0.1", "10.0.0.2"})
	srv := NewServer(ctrl)

	resp, err := srv.ListGateways(context.Background(), &haifypb.ListGatewaysRequest{})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)
	got := map[string]string{}
	for _, gw := range resp.Gateways {
		got[gw.Resource] = gw.Type
	}
	assert.Equal(t, map[string]string{"data": "nfs", "blk": "iscsi"}, got)
}

// When no node answers, the database the create path persists to is used.
func TestServerListGatewaysFallsBackToDatabase(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	require.NoError(t, ctrl.db.SaveGateway(context.Background(), &database.Gateway{
		Name: "data-nfs", Resource: "data", Type: database.GatewayTypeNFS,
	}))
	ctrl.gateway = gateway.New(nil, &listingGatewayDeployment{}, zap.NewNop(), []string{"10.0.0.1"})
	srv := NewServer(ctrl)

	resp, err := srv.ListGateways(context.Background(), &haifypb.ListGatewaysRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Gateways, 1)
	assert.Equal(t, "data", resp.Gateways[0].Resource)
}
