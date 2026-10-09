package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/deployment"
)

// The listing path end to end: vgs for capacity, lvs for utilisation, folded
// into one PoolInfo per group, and then handed to the alert monitor.

// thinClusterDeployment answers the two queries the pool listing makes. Both
// reports are cluster-wide, matching what the real client sends.
func thinClusterDeployment(vgs, thin string) *fakeDeploymentClient {
	return &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "vgs") {
				return successExecResult(hosts, vgs), nil
			}
			return successExecResult(hosts, ""), nil
		},
		lvsThinReportFunc: func(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error) {
			return successExecResult(hosts, thin), nil
		},
	}
}

func newThinTestController(t *testing.T, vgs, thin string) *Controller {
	t.Helper()
	ctrl := newBasicTestController(thinClusterDeployment(vgs, thin))
	ctrl.hosts = []string{"node-e"}
	ctrl.hostsMap = map[string]string{"node-e": "node-e"}
	return ctrl
}

func TestListPoolsFoldsInThinUsage(t *testing.T) {
	// 21470642176 bytes is what a 20 GiB disk leaves in the group after PV
	// metadata: 19.996 GiB.
	ctrl := newThinTestController(t,
		"  haify_haifypool|21470642176|0|/dev/sdb",
		"  haify_haifypool|haifythin|thin-pool|20937965568|45.50|8.23|twi-aotz--")

	pools, err := ctrl.storage.ListPools(context.Background())
	require.NoError(t, err)
	require.Len(t, pools, 1)

	p := pools[0]
	require.NotNil(t, p.ThinUsage, "utilisation must reach the pool listing")
	assert.Equal(t, "haifythin", p.ThinUsage.PoolLV)
	assert.InDelta(t, 45.50, p.ThinUsage.DataPercent, 0.001)
	assert.InDelta(t, 8.23, p.ThinUsage.MetaPercent, 0.001)

	// The truncation defect: 19.996 GiB must not present as 19 GB.
	assert.Equal(t, uint64(20), p.TotalGB)
	assert.Equal(t, uint64(21470642176), p.TotalBytes)
	assert.Zero(t, p.FreeGB, "the group really is fully allocated to the pool")
}

func TestBytesToGBRoundsRatherThanTruncates(t *testing.T) {
	const giB = uint64(1024 * 1024 * 1024)
	assert.Equal(t, uint64(20), bytesToGB(21470642176)) // 19.996 GiB
	assert.Equal(t, uint64(10), bytesToGB(10*giB))
	assert.Equal(t, uint64(0), bytesToGB(0))
	// Genuinely below the halfway point still rounds down, so the figure stays
	// an honest approximation rather than always inflating.
	assert.Equal(t, uint64(19), bytesToGB(19*giB+giB/4))
}

func TestGetPoolStatusListFeedsTheMonitor(t *testing.T) {
	ctrl := newThinTestController(t,
		"  haify_haifypool|21470642176|0|/dev/sdb",
		"  haify_haifypool|haifythin|thin-pool|20937965568|100.00|14.00|twi-aotzD-")

	statuses, err := ctrl.storage.GetPoolStatusList(context.Background())
	require.NoError(t, err)
	require.Len(t, statuses, 1)

	s := statuses[0]
	assert.Equal(t, "haify_haifypool", s.Name)
	assert.Equal(t, "haifythin", s.ThinPool)
	assert.InDelta(t, 100.0, s.DataPercent, 0.001)
	assert.True(t, s.OutOfSpace, "LVM's own flag must survive the adapter")
}

func TestGetPoolStatusListPassesThroughThickPools(t *testing.T) {
	// A group with no thin pool is reported with an empty ThinPool rather than
	// dropped: the monitor needs to see it to tell a pool converted to thick
	// from a pool that was deleted.
	ctrl := newThinTestController(t,
		"  haify_haifypool|21470642176|10737418240|/dev/sdb",
		"  haify_haifypool|data|linear|10737418240|||-wi-a-----")

	statuses, err := ctrl.storage.GetPoolStatusList(context.Background())
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Empty(t, statuses[0].ThinPool)
	assert.Equal(t, "haify_haifypool", statuses[0].Name)
}

func TestListPoolsSurvivesAThinReportFailure(t *testing.T) {
	// A pool that cannot report its utilisation is still a pool. Failing the
	// whole listing would take the page down over a missing badge.
	dep := thinClusterDeployment("  haify_haifypool|21470642176|0|/dev/sdb", "")
	dep.lvsThinReportFunc = func(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error) {
		return failedExecResult(hosts, "lvs: command not found"), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.hosts = []string{"node-e"}
	ctrl.hostsMap = map[string]string{"node-e": "node-e"}

	pools, err := ctrl.storage.ListPools(context.Background())
	require.NoError(t, err)
	require.Len(t, pools, 1)
	assert.Nil(t, pools[0].ThinUsage)
}
