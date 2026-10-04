package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const placedNFS = "[[promoter]]\n[promoter.resources.data]\nstart = [\"ocf:heartbeat:Filesystem fs_cluster_private device=/dev/drbd1\"]\n"

func placementFixture(nodeConfigs map[string]map[string]string) (*Manager, *MockDeploymentClient) {
	res := &MockResourceManager{Resources: map[string]*ResourceInfo{
		"data": {Name: "data", Hosts: []string{"n1", "n2", "n3"}, Nodes: []string{"n1", "n2", "n3"}},
	}}
	dep := &MockDeploymentClient{NodeConfigs: nodeConfigs}
	return New(res, dep, zap.NewNop(), []string{"n1", "n2", "n3", "n4"}), dep
}

// A replica added after the gateway was created had no promoter and could
// never take the gateway over. It gets the copy the others hold, and only it
// is written to: on the node serving the gateway a rewrite plus reload stops
// the whole gateway.
func TestSyncPlacementInstallsTheGatewayOnANewReplicaOnly(t *testing.T) {
	path := gatewayConfigPath("sds-nfs-data")
	m, dep := placementFixture(map[string]map[string]string{
		"n1": {path: placedNFS},
		"n2": {path + pendingSuffix: placedNFS + "# edited\n", path: placedNFS},
		"n3": {},
	})

	require.NoError(t, m.SyncPlacement(context.Background(), "data"))

	assert.Equal(t, []string{"n3"}, dep.ConfigHosts[path])
	assert.Contains(t, []string{placedNFS, placedNFS + "# edited\n"}, dep.Configs[path])
	assert.NotContains(t, dep.ConfigHosts, path+pendingSuffix, "a pending edit stays on the node running the gateway")

	// The prerequisites were checked on the newcomer before it got the config,
	// and the gateway was retired from the node that is not a replica.
	var checkedOn, retiredOn [][]string
	for i, cmd := range dep.ExecCommands {
		script := decodeScriptCmd(cmd)
		if strings.Contains(script, "missing=") {
			checkedOn = append(checkedOn, dep.ExecHosts[i])
		}
		if strings.Contains(script, "drbd-services@") && strings.Contains(script, "rm -f") {
			retiredOn = append(retiredOn, dep.ExecHosts[i])
		}
	}
	assert.Equal(t, [][]string{{"n3"}}, checkedOn)
	assert.Equal(t, [][]string{{"n4"}}, retiredOn)
}

// A stopped gateway stays stopped on the new replica.
func TestSyncPlacementKeepsAStoppedGatewayStopped(t *testing.T) {
	path := gatewayConfigPath("sds-iscsi-data")
	m, dep := placementFixture(map[string]map[string]string{
		"n1": {path + disabledSuffix: placedNFS},
		"n2": {path + disabledSuffix: placedNFS},
		"n3": {},
	})

	require.NoError(t, m.SyncPlacement(context.Background(), "data"))

	assert.Equal(t, []string{"n3"}, dep.ConfigHosts[path+disabledSuffix])
	assert.NotContains(t, dep.ConfigHosts, path)
	for _, cmd := range dep.ExecCommands {
		assert.NotEqual(t, reloadReactorCmd, cmd, "nothing to reload for a stopped gateway")
	}
}

// A newcomer that could not run the chain gets nothing.
func TestSyncPlacementRefusesANodeMissingPrerequisites(t *testing.T) {
	path := gatewayConfigPath("sds-nfs-data")
	m, dep := placementFixture(map[string]map[string]string{
		"n1": {path: placedNFS},
		"n2": {path: placedNFS},
		"n3": {},
	})
	dep.ScriptErr = map[string]error{"missing=": errors.New("missing: nfsserver")}

	err := m.SyncPlacement(context.Background(), "data")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nfsserver")
	assert.NotContains(t, dep.ConfigHosts, path)
}

// Unlike promoterHosts, an unknown resource is an error, never "every host":
// that would install a gateway on nodes holding no copy of its data.
func TestSyncPlacementNeverGuessesTheReplicas(t *testing.T) {
	m, dep := placementFixture(nil)
	require.Error(t, m.SyncPlacement(context.Background(), "other"))
	assert.Empty(t, dep.ConfigHosts)
}
