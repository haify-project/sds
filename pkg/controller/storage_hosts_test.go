package controller

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A node the registry holds offline is not asked: it would hang every query
// of the listing until the SSH connection timed out.
func TestListPoolsSkipsOfflineNodes(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]bool{}
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			mu.Lock()
			for _, h := range hosts {
				asked[h] = true
			}
			mu.Unlock()
			if strings.Contains(cmd, "vgs") {
				return successExecResult(hosts, "  haify_tp|10737418240B|5368709120B|/dev/vdb\n"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.hosts = []string{"10.0.0.1", "10.0.0.2"}
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "n1", Address: "10.0.0.1", State: NodeStateOnline}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "n2", Address: "10.0.0.2", State: NodeStateOffline}

	pools, err := ctrl.storage.ListPools(context.Background())
	require.NoError(t, err)
	assert.True(t, asked["10.0.0.1"])
	assert.False(t, asked["10.0.0.2"], "the offline node must not be asked")
	require.Len(t, pools, 1)
	assert.Equal(t, "haify_tp", pools[0].Name)
}
