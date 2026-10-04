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

func resourceSnapshotFixture(t *testing.T, failOn string) (*Controller, *[]string, *[]string) {
	t.Helper()
	var cmds, thin []string
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		cmds = append(cmds, hosts[0]+": "+cmd)
		return successExecResult(hosts, ""), nil
	}
	dep.lvIsThinFunc = func(context.Context, string, string, string) (bool, error) { return true, nil }
	dep.lvCreateThinSnapshotFunc = func(_ context.Context, hosts []string, vg, lv, name string) (*deployment.ExecResult, error) {
		thin = append(thin, hosts[0]+": "+vg+"/"+lv+"->"+name)
		if failOn != "" && hosts[0] == failOn {
			return failedExecResult(hosts, "no space"), nil
		}
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "db", Port: 7000, Nodes: "10.0.0.1,10.0.0.2"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "db", VolumeName: "db_data", Pool: "vg0",
		SizeGB: 2, Device: "/dev/vg0/db_data"}))
	return ctrl, &cmds, &thin
}

// Every replica is snapshotted, under I/O suspended everywhere with a
// watchdog armed first, and I/O resumes afterwards.
func TestResourceSnapshotOnEveryReplica(t *testing.T) {
	ctrl, cmds, thin := resourceSnapshotFixture(t, "")
	require.NoError(t, ctrl.resources.CreateResourceSnapshot(context.Background(), "db", "before"))
	assert.ElementsMatch(t, []string{"10.0.0.1: vg0/db_data->db_data_snap_before", "10.0.0.2: vg0/db_data->db_data_snap_before"}, *thin)
	joined := strings.Join(*cmds, "\n")
	for _, h := range []string{"10.0.0.1", "10.0.0.2"} {
		assert.Contains(t, joined, h+": sudo systemd-run --unit=sds-snapshot-resume-db-")
		assert.Contains(t, joined, "--on-active=60 drbdadm resume-io db && sudo drbdadm suspend-io db")
	}
	assert.True(t, strings.HasSuffix((*cmds)[len(*cmds)-1], ": sudo drbdadm resume-io db"), "I/O resumes last")

	assert.Error(t, ctrl.resources.CreateResourceSnapshot(context.Background(), "db", "bad name"))
}

// A snapshot that fails on one replica is removed from the others: a
// resource snapshot is on all of them or none.
func TestResourceSnapshotAllOrNothing(t *testing.T) {
	ctrl, cmds, _ := resourceSnapshotFixture(t, "10.0.0.2")
	err := ctrl.resources.CreateResourceSnapshot(context.Background(), "db", "before")
	require.Error(t, err)
	joined := strings.Join(*cmds, "\n")
	assert.Contains(t, joined, "sudo drbdadm resume-io db", "I/O resumes on failure too")
}

// Rolling back takes the resource down everywhere, merges every replica's
// snapshot, takes it again (a merge consumes it) and brings it back up.
func TestResourceSnapshotRollback(t *testing.T) {
	ctrl, cmds, thin := resourceSnapshotFixture(t, "")
	require.NoError(t, ctrl.resources.RollbackResourceSnapshot(context.Background(), "db", "before"))
	joined := strings.Join(*cmds, "\n")
	down := strings.Index(joined, "drbdadm down db")
	merge := strings.Index(joined, "lvconvert --merge -y vg0/db_data_snap_before")
	up := strings.Index(joined, "drbdadm up db")
	require.True(t, down >= 0 && merge > down && up > merge, joined)
	assert.Equal(t, 2, strings.Count(joined, "lvconvert --merge"), "on every replica")
	assert.Len(t, *thin, 2, "the snapshot is kept")
}
