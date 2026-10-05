package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
	"github.com/haify-project/sds/pkg/event"
)

func snapAt(ts time.Time) string { return buildSnapName("data_data", ts) }

func TestSnapshotLockWindow(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	fresh, old := snapAt(now.Add(-24*time.Hour)), snapAt(now.Add(-8*24*time.Hour))

	assert.Equal(t, now.Add(6*24*time.Hour), snapshotLockedUntil(fresh, week, now))
	assert.True(t, snapshotLockedUntil(old, week, now).IsZero())
	assert.True(t, snapshotLockedUntil("manual-snap", week, now).IsZero(), "only scheduled snapshots are locked")
	assert.True(t, snapshotLockedUntil(fresh, 0, now).IsZero())

	left := unlockedSnaps([]scheduledSnap{{Name: fresh}, {Name: old}}, week, now)
	require.Len(t, left, 1)
	assert.Equal(t, old, left[0].Name)
}

// The lock may go up at any time, never down while something is locked:
// lowering it would unlock the snapshots it protects.
func TestScheduleLockChange(t *testing.T) {
	now := time.Now()
	old := &database.SnapshotSchedule{Resource: "data", LockDays: 14, LastRun: now.Add(-time.Hour)}
	require.NoError(t, checkScheduleLockChange(old, 30, now))
	assert.ErrorContains(t, checkScheduleLockChange(old, 7, now), "can be raised but not lowered")
	assert.ErrorContains(t, checkScheduleLockChange(old, 0, now), "not lowered")

	stale := &database.SnapshotSchedule{Resource: "data", LockDays: 14, LastRun: now.AddDate(0, 0, -15)}
	require.NoError(t, checkScheduleLockChange(stale, 0, now), "nothing is locked any more")
	assert.Error(t, checkScheduleLockChange(nil, 400, now), "a typo cannot lock a pool for a decade")
}

func lockedScheduleController(t *testing.T, dep *fakeDeploymentClient) *Controller {
	t.Helper()
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "data", Port: 7000, Nodes: "n1,n2"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "data", VolumeName: "data_data", Pool: "vg0"}))
	lock := 7
	require.NoError(t, ctrl.schedules.CreateSchedule(ctx, "data", "0 * * * *", database.GFSPolicy{Hourly: 2}, true, &lock))
	s, err := ctrl.db.GetSnapshotSchedule(ctx, "data")
	require.NoError(t, err)
	s.LastRun = time.Now().Add(-time.Hour)
	require.NoError(t, ctrl.db.SaveSnapshotSchedule(ctx, s))
	return ctrl
}

// With an sds token, deleting the snapshots, the schedule or the resource was
// as good as deleting the history. While anything is locked, none of it goes.
func TestLockedSnapshotsResistTheAPI(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := lockedScheduleController(t, dep)
	ctx := context.Background()
	fresh := snapAt(time.Now().Add(-time.Hour))

	assert.ErrorContains(t, ctrl.snapshots.DeleteSnapshot(ctx, "vg0/data_data", fresh, "n1"), "is locked")
	assert.ErrorContains(t, ctrl.storage.DeleteLvmSnapshot(ctx, "vg0", fresh, "n1"), "is locked")
	assert.ErrorContains(t, ctrl.storage.ZFSDeleteSnapshot(ctx, "tank/data_data@"+fresh, "n1"), "is locked")
	assert.Empty(t, dep.execCalls, "nothing reached a node")

	assert.ErrorContains(t, ctrl.schedules.DeleteSchedule(ctx, "data"), "locked until")
	assert.ErrorContains(t, ctrl.resources.DeleteResource(ctx, "data", true), "deleting the resource", "not even --force")
	assert.ErrorContains(t, ctrl.resources.RemoveVolume(ctx, "data", 1), "removing a volume")

	// Removing or moving a replica deletes its storage, snapshots included:
	// one node at a time, that was every locked snapshot gone.
	r, err := ctrl.db.GetResource(ctx, "data")
	require.NoError(t, err)
	r.Nodes = "n1,n2,n3"
	require.NoError(t, ctrl.db.SaveResource(ctx, r))
	assert.ErrorContains(t, ctrl.resources.RemoveReplica(ctx, "data", "n2"), "removing the replica on n2")
	assert.ErrorContains(t, ctrl.resources.MoveReplica(ctx, "data", "n2", "n4"), "moving the replica off n2")
	assert.Empty(t, dep.execCalls, "nothing reached a node")

	zero := 0
	assert.ErrorContains(t, ctrl.schedules.CreateSchedule(ctx, "data", "0 * * * *", database.GFSPolicy{Hourly: 2}, true, &zero), "not lowered")
	// Replacing the schedule without naming a lock keeps it, and its last run.
	require.NoError(t, ctrl.schedules.CreateSchedule(ctx, "data", "30 * * * *", database.GFSPolicy{Hourly: 2}, true, nil))
	s, err := ctrl.db.GetSnapshotSchedule(ctx, "data")
	require.NoError(t, err)
	assert.Equal(t, 7, s.LockDays)
	assert.False(t, s.LastRun.IsZero(), "the last run is what dates the newest lock")

	old := snapAt(time.Now().AddDate(0, 0, -8))
	assert.NotContains(t, fmt.Sprint(ctrl.snapshots.DeleteSnapshot(ctx, "vg0/data_data", old, "n1")), "locked",
		"a snapshot past its lock is an ordinary delete")
}

// A pool filling fast is what encryption looks like. Its oldest snapshots are
// the clean ones, and locked ones stay; the operator hears about both.
func TestRelieveThinPoolKeepsLockedSnapshots(t *testing.T) {
	now := time.Now().UTC()
	snaps := []string{
		snapAt(now.AddDate(0, 0, -20)), snapAt(now.AddDate(0, 0, -3)),
		snapAt(now.AddDate(0, 0, -2)), snapAt(now.AddDate(0, 0, -1)), snapAt(now.Add(-time.Hour)),
	}
	var removed []string
	dep := &fakeDeploymentClient{}
	dep.lvsThinReportFunc = func(_ context.Context, hosts []string, vg string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, fmt.Sprintf("  %s|pool_thin|thin-pool|21474836480|99.00|10.00|twi-aotz--", vg)), nil
	}
	dep.lvListSnapshotsFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		var lines []string
		for _, s := range snaps {
			if !contains(removed, s) {
				lines = append(lines, s+"|1|x|data_data")
			}
		}
		return successExecResult(hosts, strings.Join(lines, "\n")), nil
	}
	dep.lvRemoveSnapshotFunc = func(_ context.Context, hosts []string, _, name string) (*deployment.ExecResult, error) {
		removed = append(removed, name)
		return successExecResult(hosts, ""), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.events = event.NewBus(10)
	vol := &ResourceVolumeInfo{Pool: "sds_pool", BackingVolume: "data_data", Device: "/dev/sds_pool/data_data"}

	ctrl.schedules.relieveThinPool(context.Background(), "n1", "n1", "data", vol, 7*24*time.Hour)
	assert.Equal(t, []string{snaps[0]}, removed, "only the snapshot past its lock may go")

	events := ctrl.events.Recent(event.Filter{}, 0, 10)
	var types []event.Type
	for _, e := range events {
		types = append(types, e.Type)
	}
	assert.Contains(t, types, event.TypePoolSnapshotsRemoved, "history going is announced")
	assert.Contains(t, types, event.TypePoolSnapshotsLocked, "and a pool left full with locked snapshots is critical")
}

// zfs rollback -r destroys every later snapshot; a locked one stops it.
func TestZFSRollbackKeepsLockedSnapshots(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := lockedScheduleController(t, dep)
	older, newer := snapAt(time.Now().AddDate(0, 0, -10)), snapAt(time.Now().Add(-time.Hour))
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "zfs list -t snapshot") {
			return successExecResult(hosts, "tank/data_data@"+older+"\ntank/data_data@"+newer+"\n"), nil
		}
		return successExecResult(hosts, ""), nil
	}
	err := ctrl.snapshots.RestoreZFSSnapshot(context.Background(), "tank/data_data", older, "n1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), newer+" is locked")
}

// Restoring merges the snapshot away; a locked thin one is taken again under
// its own name, a locked thick one is refused.
func TestRestoreKeepsALockedThinSnapshot(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := lockedScheduleController(t, dep)
	fresh := snapAt(time.Now().Add(-time.Hour))
	var retaken []string
	dep.lvCreateThinSnapshotFunc = func(_ context.Context, hosts []string, vg, lv, name string) (*deployment.ExecResult, error) {
		retaken = append(retaken, vg+"/"+lv+"->"+name)
		return successExecResult(hosts, ""), nil
	}

	dep.lvIsThinFunc = func(context.Context, string, string, string) (bool, error) { return true, nil }
	merge, err := ctrl.snapshots.lockPreservingMerge(context.Background(), "n1", "vg0", "data_data", fresh, "/dev/vg0/"+fresh, "/dev/vg0/data_data")
	require.NoError(t, err)
	require.NoError(t, merge())
	assert.Equal(t, []string{"vg0/data_data->" + fresh}, retaken)

	dep.lvIsThinFunc = func(context.Context, string, string, string) (bool, error) { return false, nil }
	_, err = ctrl.snapshots.lockPreservingMerge(context.Background(), "n1", "vg0", "data_data", fresh, "/dev/vg0/"+fresh, "/dev/vg0/data_data")
	assert.ErrorContains(t, err, "only a locked thin snapshot")
}
