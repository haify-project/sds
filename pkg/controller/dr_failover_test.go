package controller

import (
	"context"
	"testing"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drPromoteFixture registers a two-node resource and a DR peer that keeps
// believing the lost primary is Primary until it is disconnected.
func drPromoteFixture(t *testing.T, wan bool) (*Controller, *fakeDeploymentClient) {
	t.Helper()
	disconnected := false
	dep := &fakeDeploymentClient{}
	dep.drbdPrimaryFunc = func(_ context.Context, host, resource string, force bool) (*deployment.HostResult, error) {
		if !disconnected {
			return &deployment.HostResult{Host: host, Output: "wan1: State change failed: (-1) Multiple primaries not allowed by config"}, nil
		}
		return &deployment.HostResult{Host: host, Success: true}, nil
	}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if cmd == "sudo drbdadm disconnect --force wan1" {
			disconnected = true
		}
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctrl.hostsMap["prim"] = "10.0.0.1"
	ctrl.hostsMap["dr"] = "10.0.0.9"
	rec := &database.Resource{Name: "wan1", Nodes: "prim,dr", Port: 7150}
	if wan {
		rec.WANMode, rec.DRNode, rec.DREndpoint = true, "dr", "10.0.0.9"
	}
	require.NoError(t, ctrl.db.SaveResource(context.Background(), rec))
	return ctrl, dep
}

// A DR node that has not yet noticed the primary site is gone refuses a forced
// promote. The DR failover must cut it loose and promote, not report
// "Multiple primaries not allowed" for the first half-minute of a disaster.
func TestForcedPromoteOfDRNodeDisconnectsFromThePrimarySite(t *testing.T) {
	ctrl, dep := drPromoteFixture(t, true)
	require.NoError(t, ctrl.resources.SetPrimary(context.Background(), "wan1", "dr", true))
	var sawDisconnect bool
	for _, c := range dep.execCalls {
		if c.cmd == "sudo drbdadm disconnect --force wan1" {
			sawDisconnect = true
			assert.Equal(t, []string{"10.0.0.9"}, c.hosts)
		}
	}
	assert.True(t, sawDisconnect)
}

// Nowhere else is a peer that still looks Primary a reason to disconnect.
func TestForcedPromoteOutsideDRNeverDisconnects(t *testing.T) {
	ctrl, dep := drPromoteFixture(t, false)
	err := ctrl.resources.SetPrimary(context.Background(), "wan1", "dr", true)
	require.Error(t, err)
	for _, c := range dep.execCalls {
		assert.NotEqual(t, "sudo drbdadm disconnect --force wan1", c.cmd)
	}

	ctrl, dep = drPromoteFixture(t, true)
	require.Error(t, ctrl.resources.SetPrimary(context.Background(), "wan1", "prim", true))
	for _, c := range dep.execCalls {
		assert.NotEqual(t, "sudo drbdadm disconnect --force wan1", c.cmd)
	}
}
