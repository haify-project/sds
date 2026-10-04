package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

func TestRenameInConfig(t *testing.T) {
	conf := "resource pve-100-0 {\n    volume 0 {\n        disk      /dev/sds_vg0/pve-100-0_data;\n    }\n" +
		"    on n1 {\n        address 10.0.0.1:7000;\n    }\n}\n"
	out, err := renameInConfig(conf, "pve-100-0", "pve-base-100-0",
		map[string]string{"pve-100-0_data": "pve-base-100-0_data"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "resource pve-base-100-0 {"))
	assert.Contains(t, out, "/dev/sds_vg0/pve-base-100-0_data;")
	assert.NotContains(t, out, "pve-100-0_data")

	_, err = renameInConfig(conf, "other", "x", nil)
	assert.Error(t, err)
	assert.Equal(t, "new_vol1", renamedBacking("old_vol1", "old", "new"))
}

func renameFixture(t *testing.T) (*Controller, *fakeDeploymentClient, *[]string) {
	t.Helper()
	var cmds []string
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		cmds = append(cmds, cmd)
		if strings.HasPrefix(cmd, "cat /etc/drbd.d/") {
			return successExecResult(hosts, "resource old {\n    volume 0 {\n        disk /dev/sds_vg0/old_data;\n    }\n}\n"), nil
		}
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "old", Port: 7000, Nodes: "n1,n2",
		Labels: map[string]string{pveManagedByLabel: "pve"}}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "old", VolumeName: "old_data",
		Pool: "sds_vg0", SizeGB: 2, Device: "/dev/sds_vg0/old_data"}))
	return ctrl, dep, &cmds
}

// The resource goes down, its backing volume and config are renamed, it comes
// up under the new name, and the records follow with labels and size intact.
func TestRenameResource(t *testing.T) {
	ctrl, _, cmds := renameFixture(t)
	ctx := context.Background()
	require.NoError(t, ctrl.resources.RenameResource(ctx, "old", "new"))

	joined := strings.Join(*cmds, "\n")
	down := strings.Index(joined, "drbdadm down old")
	lvrename := strings.Index(joined, "lvrename sds_vg0 old_data new_data")
	up := strings.Index(joined, "drbdadm up new")
	require.True(t, down >= 0 && lvrename > down && up > lvrename, joined)

	_, err := ctrl.db.GetResource(ctx, "old")
	assert.Error(t, err)
	res, err := ctrl.db.GetResource(ctx, "new")
	require.NoError(t, err)
	assert.Equal(t, "pve", res.Labels[pveManagedByLabel])
	vols, _ := ctrl.db.ListVolumes(ctx, "new")
	require.Len(t, vols, 1)
	assert.Equal(t, "new_data", vols[0].VolumeName)
	assert.Equal(t, "/dev/sds_vg0/new_data", vols[0].Device)
}

func TestRenameRefusesWhatRefersToTheName(t *testing.T) {
	ctrl, _, cmds := renameFixture(t)
	ctx := context.Background()
	assert.ErrorContains(t, ctrl.resources.RenameResource(ctx, "old", "bad name"), "not a valid")
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "taken", Port: 7001, Nodes: "n1"}))
	assert.ErrorContains(t, ctrl.resources.RenameResource(ctx, "old", "taken"), "already exists")
	require.NoError(t, ctrl.db.SaveSnapshotSchedule(ctx, &database.SnapshotSchedule{Name: "old", Resource: "old", Cron: "0 * * * *"}))
	assert.ErrorContains(t, ctrl.resources.RenameResource(ctx, "old", "new"), "snapshot schedule")
	for _, c := range *cmds {
		assert.NotContains(t, c, "drbdadm down", "nothing was touched")
	}
}
