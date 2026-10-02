package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/gateway"
)

// fakeGatewayDeployment records the commands the gateway Manager issues so a
// teardown can be asserted without touching real nodes. It implements
// gateway.DeploymentClient.
type fakeGatewayDeployment struct {
	execCmds []string
}

func (f *fakeGatewayDeployment) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string) error {
	return nil
}

func (f *fakeGatewayDeployment) Exec(ctx context.Context, hosts []string, cmd string) error {
	f.execCmds = append(f.execCmds, cmd)
	return nil
}

// TestDeleteResourceCascadesGatewayTeardown asserts that deleting a resource
// that still owns a gateway record tears the gateway down first: it runs the
// gateway Manager teardown (removing the reactor config on every node) and
// drops the gateway DB record, so no live orphan (reactor promoter + target +
// UP DRBD device) is left behind. Mirrors the HA-cascade behaviour.
func TestDeleteResourceCascadesGatewayTeardown(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"orange1": "10.0.0.1",
		"orange2": "10.0.0.2",
	})

	gwDep := &fakeGatewayDeployment{}
	ctrl.gateway = gateway.New(nil, gwDep, zap.NewNop(), []string{"10.0.0.1", "10.0.0.2"})

	db := newTestDB(t)
	ctrl.db = db

	require.NoError(t, db.SaveResource(context.Background(), &database.Resource{
		Name:     "data",
		Port:     7100,
		Nodes:    "orange1,orange2",
		Protocol: "C",
		Replicas: 2,
	}))
	require.NoError(t, db.SaveGateway(context.Background(), &database.Gateway{
		Name:     "data",
		Resource: "data",
		Type:     database.GatewayTypeNFS,
		Status:   "running",
	}))

	require.NoError(t, ctrl.resources.DeleteResource(context.Background(), "data", true))

	// The gateway DB record must be gone.
	_, err := db.GetGatewayByResource(context.Background(), "data")
	require.Error(t, err)

	// The gateway Manager teardown must have removed its reactor config on the
	// nodes (proof the promoter/target orphan is torn down, not just the DB row).
	var removedConfig, reloaded bool
	for _, cmd := range gwDep.execCmds {
		if strings.Contains(cmd, "rm -f") && strings.Contains(cmd, "sds-nfs-data.toml") {
			removedConfig = true
		}
		if strings.Contains(cmd, "drbd-reactor") {
			reloaded = true
		}
	}
	assert.True(t, removedConfig, "expected gateway reactor config removal, got cmds: %v", gwDep.execCmds)
	assert.True(t, reloaded, "expected drbd-reactor reload during gateway teardown")

	// The resource must still be brought down and its config removed afterwards.
	require.Len(t, dep.drbdDownCalls, 1)
	require.Len(t, dep.deleteConfigCalls, 1)
}

// TestDeleteResourceNoGatewayIsNoOp ensures the gateway cascade is idempotent:
// a resource with no gateway record deletes cleanly and never invokes gateway
// teardown.
func TestDeleteResourceNoGatewayIsNoOp(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"orange1": "10.0.0.1",
		"orange2": "10.0.0.2",
	})

	gwDep := &fakeGatewayDeployment{}
	ctrl.gateway = gateway.New(nil, gwDep, zap.NewNop(), []string{"10.0.0.1", "10.0.0.2"})

	db := newTestDB(t)
	ctrl.db = db

	require.NoError(t, db.SaveResource(context.Background(), &database.Resource{
		Name:     "plain",
		Port:     7200,
		Nodes:    "orange1,orange2",
		Protocol: "C",
		Replicas: 2,
	}))

	require.NoError(t, ctrl.resources.DeleteResource(context.Background(), "plain", true))

	assert.Empty(t, gwDep.execCmds, "gateway teardown must not run when no gateway record exists")
}
