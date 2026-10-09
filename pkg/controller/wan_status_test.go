package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The legs of a multi-replica WAN resource are named after the primary-site
// nodes (haify-proxy@<res>_<node>), and status must probe exactly those units.
// Deriving the name from the node's address instead found no such unit and
// reported every running tunnel as inactive.
func TestWANStatusProbesTheProvisionedLegNames(t *testing.T) {
	ctx := context.Background()
	running := map[string]bool{"haify-proxy@wan2_n1": true, "haify-proxy@wan2_n2": true}
	dep := &fakeDeploymentClient{execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if unit, ok := strings.CutPrefix(cmd, "systemctl is-active "); ok {
			unit = strings.Fields(unit)[0]
			if running[unit] {
				return successExecResult(hosts, "active"), nil
			}
			return successExecResult(hosts, "inactive"), nil
		}
		return successExecResult(hosts, ""), nil
	}}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctrl.hostsMap["n1"] = "10.0.0.1"
	ctrl.hostsMap["n2"] = "10.0.0.2"
	ctrl.hostsMap["dr"] = "10.0.0.3"
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{
		Name: "wan2", Nodes: "n1,n2,dr", WANMode: true, DRNode: "dr",
		DREndpoint: "203.0.113.2", WANPort: 43516, Port: 7001,
	}))

	status, err := ctrl.resources.WANStatus(ctx, "wan2")
	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, map[string]string{
		"n1":          "active",
		"n2":          "active",
		"dr (leg n1)": "active",
		"dr (leg n2)": "active",
	}, status.ProxyState)
}
