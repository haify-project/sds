package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/deployment"
)

// A standby's drbd-reactor reports a Primary it cannot place as "unknown".
// Taking that for a node name made `ha evict sds-meta` fail with
// "failed to resolve host unknown" whenever the controller sat on a node the
// standbys could not name; the lookup has to go on to the DRBD status.
func TestFindActiveNodeIgnoresAnUnknownPrimary(t *testing.T) {
	dep := &fakeDeploymentClient{
		reactorPromoterStatusByResourceFunc: func(ctx context.Context, host, resource string) (*deployment.ReactorPromoterStatus, error) {
			return &deployment.ReactorPromoterStatus{PrimaryOn: "unknown"}, nil
		},
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			return successExecResult(hosts, "sds-meta role:Secondary\n  node-b role:Primary\n  node-e role:Secondary\n"), nil
		},
	}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{"node-b": "10.0.0.2", "node-e": "10.0.0.3"})

	got, err := ctrl.resources.findActiveNode(context.Background(), "sds-meta", []string{"10.0.0.3"})
	require.NoError(t, err)
	assert.NotEqual(t, "unknown", got)
	assert.Contains(t, []string{"node-b", "10.0.0.2"}, got)
}
