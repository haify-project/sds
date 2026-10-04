package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// A locked schedule holds every ZFS snapshot it takes, so ZFS itself refuses
// a destroy on the node; sds releases the hold only when it deletes a
// snapshot whose lock has passed.
func TestLockedZFSSnapshotsAreHeld(t *testing.T) {
	var cmds []string
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		cmds = append(cmds, cmd)
		return successExecResult(hosts, ""), nil
	}
	ctrl := lockedScheduleController(t, dep)
	vol := &ResourceVolumeInfo{Pool: "tank", BackingVolume: "data_data", Device: "/dev/zvol/tank/data_data"}
	ts := time.Now().UTC()
	ctrl.schedules.snapshotVolume(context.Background(), "n1", "n1", vol, ts)
	ctrl.holdLockedZFSSnapshot(context.Background(), "n1", "tank/data_data@"+buildSnapName("data_data", ts))
	require.NotEmpty(t, cmds)
	assert.Equal(t, "sudo zfs hold sds-lock tank/data_data@"+buildSnapName("data_data", ts), cmds[len(cmds)-1])

	cmds = nil
	old := snapAt(time.Now().AddDate(0, 0, -8))
	require.NoError(t, ctrl.storage.ZFSDeleteSnapshot(context.Background(), "tank/data_data@"+old, "n1"))
	require.NotEmpty(t, cmds)
	assert.Contains(t, cmds[0], "sudo zfs release sds-lock sds_tank/data_data@"+old, "the hold goes right before the delete")
}

func TestZFSSnapshotRefRejectsShell(t *testing.T) {
	_, err := zfsSnapshotRef("tank/x@a;rm -rf /")
	assert.Error(t, err)
	_, err = zfsSnapshotRef("tank/x")
	assert.Error(t, err)
	ref, err := zfsSnapshotRef("tank/x@data_sched_20261004T120000Z")
	require.NoError(t, err)
	assert.False(t, strings.ContainsAny(ref, " ;|&$"))
}

// Locks are judged by the time counted since the controller started, which a
// change of the system clock does not move.
func TestLockClock(t *testing.T) {
	assert.InDelta(t, 0, clockJump(time.Now(), lockNow()).Seconds(), 1)
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, 365*24*time.Hour, clockJump(start.AddDate(1, 0, 0), start), "a year forward is a jump")

	// A locked backup stays locked by the lock clock.
	b := &database.Backup{LockMode: "compliance", RetainUntil: lockNow().Add(time.Hour)}
	_, err := assertDeletable(b, false, lockNow())
	assert.Error(t, err)
}
