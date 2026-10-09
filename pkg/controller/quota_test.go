package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/config"
	"github.com/haify-project/haify/pkg/database"
)

// The report sums the thin volumes, not their snapshots, into the pool's
// virtual size.
func TestThinReportVirtualSize(t *testing.T) {
	out := "  vg0|pool_thin|thin-pool|107374182400|10.00|5.00|twi-aotz--|\n" +
		"  vg0|a_data|thin|53687091200|||Vwi-aotz--|\n" +
		"  vg0|b_data|thin|161061273600|||Vwi-aotz--|\n" +
		"  vg0|a_data_snap_x|thin|53687091200|||Vwi---tz-k|a_data\n"
	info := parseThinReport(out)["vg0"]
	require.NotNil(t, info)
	assert.EqualValues(t, 214748364800, info.VirtualBytes)
}

func TestOvercommitted(t *testing.T) {
	c := poolCapacity{thin: true, known: true, sizeBytes: 100 << 30, virtualBytes: 250 << 30, maxOvercommit: 3}
	assert.False(t, c.overcommitted(50), "300 GB on a 100 GB pool is exactly 3x")
	assert.True(t, c.overcommitted(51))
	assert.False(t, c.admits(51) && c.overcommitted(51))
	c.maxOvercommit = 0
	assert.False(t, c.overcommitted(10000), "unlimited by default")
}

// A project's resources are counted once each, at their size; a new one past
// either limit is refused, and resources outside the project do not count.
func TestProjectQuota(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctrl.config = &config.Config{Quota: config.QuotaConfig{ProjectLabel: "project",
		Projects: []config.ProjectQuota{{Name: "team-a", MaxGB: 100, MaxResources: 2}}}}
	ctx := context.Background()
	for _, r := range []struct {
		name, project string
		gb            int
	}{{"a1", "team-a", 60}, {"b1", "team-b", 500}} {
		require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: r.name, Port: 7000, Nodes: "n1,n2",
			Labels: map[string]string{"project": r.project}}))
		require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: r.name, VolumeName: r.name + "_data", SizeGB: r.gb}))
	}
	team := map[string]string{"project": "team-a"}
	require.NoError(t, ctrl.resources.assertProjectQuota(ctx, team, 40, true))
	assert.ErrorContains(t, ctrl.resources.assertProjectQuota(ctx, team, 41, true), "60 GB of its 100 GB")
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "a2", Port: 7001, Nodes: "n1", Labels: team}))
	assert.ErrorContains(t, ctrl.resources.assertProjectQuota(ctx, team, 1, true), "2 of its 2 resources")
	require.NoError(t, ctrl.resources.assertProjectQuota(ctx, map[string]string{"project": "team-b"}, 1000, true),
		"a project without a quota is unlimited")
}
