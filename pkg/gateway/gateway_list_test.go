package gateway

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// execOnlyDeployment is a DeploymentClient that cannot return node output.
type execOnlyDeployment struct{}

func (execOnlyDeployment) DistributeConfig(context.Context, []string, string, string) error {
	return nil
}
func (execOnlyDeployment) Exec(context.Context, []string, string) error { return nil }

// ListGateways must report what the nodes hold, merged across them, whatever
// the controller's own /etc/drbd-reactor.d contains.
func TestListGatewaysReadsTheNodes(t *testing.T) {
	dep := &MockDeploymentClient{HostOutputs: map[string]string{
		"node1": "haify-nfs-data.toml\nhaify-ha-db.toml\nother.toml\nhaify-iscsi-blk.toml.disabled\nhaify-nvmeof-fast.toml.disabled\n",
		"node2": "haify-iscsi-blk.toml.disabled\nhaify-nvmeof-fast.toml\nhaify-nfs-my-share.toml\n",
		// node3 did not answer.
	}}
	m := New(nil, dep, zap.NewNop(), []string{"node1", "node2", "node3"})

	gws, err := m.ListGateways(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"node1", "node2", "node3"}, dep.ExecHosts[0])
	assert.Contains(t, dep.ExecCommands[0], DrbdReactorConfigDir)

	got := map[string]string{}
	for _, gw := range gws {
		got[gw.Type+"/"+gw.Resource] = gw.State
		assert.Equal(t, gw.Resource, gw.ID)
	}
	assert.Equal(t, map[string]string{
		"iscsi/blk":    "stopped", // only the disabled copy anywhere
		"nfs/data":     "",
		"nfs/my-share": "", // a resource name may contain '-'
		"nvmeof/fast":  "", // live on node2 wins over disabled on node1
	}, got)
	assert.Equal(t, "blk", gws[0].Resource, "sorted by resource")
}

func TestListGatewaysErrors(t *testing.T) {
	ctx := context.Background()

	_, err := New(nil, &MockDeploymentClient{}, zap.NewNop(), nil).ListGateways(ctx)
	assert.ErrorContains(t, err, "no managed nodes")

	_, err = New(nil, execOnlyDeployment{}, zap.NewNop(), []string{"node1"}).ListGateways(ctx)
	assert.ErrorContains(t, err, "cannot read node output")

	_, err = New(nil, &MockDeploymentClient{}, zap.NewNop(), []string{"node1"}).ListGateways(ctx)
	assert.ErrorContains(t, err, "no managed node answered")

	dep := &MockDeploymentClient{ExecErr: fmt.Errorf("ssh down")}
	_, err = New(nil, dep, zap.NewNop(), []string{"node1"}).ListGateways(ctx)
	assert.ErrorContains(t, err, "ssh down")
}

func TestGetGatewayFromNodes(t *testing.T) {
	dep := &MockDeploymentClient{HostOutputs: map[string]string{"node1": "haify-nfs-data.toml\n"}}
	m := New(nil, dep, zap.NewNop(), []string{"node1"})

	gw, err := m.GetGateway(context.Background(), "data")
	require.NoError(t, err)
	assert.Equal(t, "nfs", gw.Type)

	_, err = m.GetGateway(context.Background(), "nope")
	assert.ErrorContains(t, err, "gateway not found")
}

func TestParseGatewayConfigName(t *testing.T) {
	for name, want := range map[string]bool{
		"haify-nfs-data.toml":            true,
		"haify-nvmeof-a-b.toml.disabled": true,
		"haify-ha-data.toml":             false,
		"haify-nfs-.toml":                false,
		"haify-nfs-data.conf":            false,
		"nfs-data.toml":                  false,
	} {
		gw, _ := parseGatewayConfigName(name)
		assert.Equal(t, want, gw != nil, name)
	}
}
