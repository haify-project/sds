package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/database"
)

func backupAt(id, kind, parent string, day int) *database.Backup {
	return &database.Backup{
		ID: id, Kind: kind, Parent: parent, State: database.BackupStateCompleted, Schedule: "s",
		StartedAt: time.Date(2026, 9, day, 2, 30, 0, 0, time.UTC),
	}
}

func ids(bs []*database.Backup) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.ID)
	}
	return out
}

// Keeping the last two daily backups keeps the full one they are built on,
// however old, and drops only what nothing kept depends on.
func TestRetentionNeverBreaksAChain(t *testing.T) {
	inc := database.BackupKindIncremental
	backups := []*database.Backup{
		backupAt("full1", database.BackupKindFull, "", 1),
		backupAt("inc2", inc, "full1", 2),
		backupAt("inc3", inc, "inc2", 3),
		backupAt("full4", database.BackupKindFull, "", 4),
		backupAt("inc5", inc, "full4", 5),
		backupAt("inc6", inc, "inc5", 6),
		backupAt("inc7", inc, "inc6", 7),
	}
	expired := selectExpiredBackups(backups, database.GFSPolicy{Daily: 2}, "s")
	assert.Equal(t, []string{"inc3", "inc2", "full1"}, ids(expired), "newest first, so no backup goes before what builds on it")

	// Keeping one more day reaches into the second chain only.
	expired = selectExpiredBackups(backups, database.GFSPolicy{Daily: 4}, "s")
	assert.Equal(t, []string{"inc3", "inc2", "full1"}, ids(expired))

	// A kept day inside the old chain keeps that chain down to its full one.
	expired = selectExpiredBackups(backups, database.GFSPolicy{Daily: 5}, "s")
	assert.Empty(t, expired)
}

func TestRetentionIgnoresRunningAndFailedRecords(t *testing.T) {
	running := backupAt("running", database.BackupKindFull, "", 1)
	running.State = database.BackupStateRunning
	failed := backupAt("failed", database.BackupKindFull, "", 2)
	failed.State = database.BackupStateFailed
	expired := selectExpiredBackups([]*database.Backup{running, failed, backupAt("ok", database.BackupKindFull, "", 3)},
		database.GFSPolicy{Daily: 1}, "s")
	assert.Empty(t, expired)
}

// A schedule deletes only what it took. A manual full backup a scheduled
// incremental is built on stays as long as that incremental does, and a
// manual backup nothing depends on is never pruned at all.
func TestRetentionLeavesBackupsTakenByHand(t *testing.T) {
	manual := backupAt("manual1", database.BackupKindFull, "", 1)
	manual.Schedule = ""
	lone := backupAt("manual3", database.BackupKindFull, "", 3)
	lone.Schedule = ""
	backups := []*database.Backup{
		manual,
		backupAt("inc2", database.BackupKindIncremental, "manual1", 2),
		lone,
		backupAt("full4", database.BackupKindFull, "", 4),
	}
	expired := selectExpiredBackups(backups, database.GFSPolicy{Daily: 1}, "s")
	assert.Equal(t, []string{"inc2"}, ids(expired))

	expired = selectExpiredBackups(backups, database.GFSPolicy{Daily: 2}, "s")
	assert.Empty(t, expired, "the kept incremental keeps the manual full it is built on")
}

func TestScheduledRunRecordsItsOutcomeAndPrunes(t *testing.T) {
	f := newIncrementalFixture(t)
	ctx := context.Background()
	sm := f.ctrl.schedules
	sc, err := sm.CreateBackupSchedule(ctx, "data", "offsite", "30 2 * * *", database.GFSPolicy{Hourly: 1}, true)
	require.NoError(t, err)
	assert.Equal(t, "data@offsite", sc.Name)

	_, err = sm.CreateBackupSchedule(ctx, "data", "nowhere", "30 2 * * *", database.GFSPolicy{Daily: 1}, true)
	require.Error(t, err, "an unknown target is refused when the schedule is saved, not at 02:30")

	// A scheduled full backup, then a fresh chain: keeping one hourly backup
	// drops the old chain once nothing kept is built on it. A backup taken by
	// hand in between is not the schedule's to delete.
	firstSc, err := sm.RunBackupSchedule(ctx, sc.Name)
	require.NoError(t, err)
	first, err := f.ctrl.db.GetBackup(ctx, firstSc.LastBackup)
	require.NoError(t, err)
	assert.Equal(t, sc.Name, first.Schedule)
	f.cmds = append(f.cmds, "ran")
	manual := f.backup(t, true)
	time.Sleep(1100 * time.Millisecond)
	f.gone[first.Volumes[0].Snapshot] = true
	got, err := sm.RunBackupSchedule(ctx, sc.Name)
	require.NoError(t, err)
	require.Empty(t, got.LastError)
	require.NotEmpty(t, got.LastBackup)

	left, err := f.ctrl.db.ListBackups(ctx, "data", "offsite")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{got.LastBackup, manual.ID}, ids(left))
}

func TestScheduledRunFailureIsRecorded(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes, uploadFails: true}
	ctrl := newBackupFixture(t, stub, "Secondary")
	ctx := context.Background()
	sc, err := ctrl.schedules.CreateBackupSchedule(ctx, "data", "offsite", "0 * * * *", database.GFSPolicy{Daily: 7}, true)
	require.NoError(t, err)

	got, err := ctrl.schedules.RunBackupSchedule(ctx, sc.Name)
	require.NoError(t, err)
	assert.Empty(t, got.LastBackup)
	assert.Contains(t, got.LastError, "Input/output error")
	stored, err := ctrl.db.GetBackupSchedule(ctx, sc.Name)
	require.NoError(t, err)
	assert.Equal(t, got.LastError, stored.LastError)
}

// A thick snapshot reserves its copy-on-write area up front; the replica whose
// volume group cannot hold it is passed over for one that can.
func TestBackupSkipsAReplicaWithoutRoomForAThickSnapshot(t *testing.T) {
	dep := &fakeDeploymentClient{
		lvIsThinFunc: func(context.Context, string, string, string) (bool, error) { return false, nil },
		vgFreeBytesFunc: func(host, vg string) (uint64, error) {
			if host == "full" {
				return 1000 << 20, nil
			}
			return 14 << 30, nil
		},
	}
	bm := &BackupManager{controller: newBasicTestController(dep)}
	info := &ResourceInfo{
		Name: "data", Nodes: []string{"full", "roomy", "primary"},
		Volumes: []*ResourceVolumeInfo{{VolumeID: 0, Pool: "vg0", BackingVolume: "data_data", SizeGB: 5}},
		NodeStates: map[string]*ResourceNodeState{
			"full":    {Role: "Secondary", DiskState: "UpToDate"},
			"roomy":   {Role: "Secondary", DiskState: "UpToDate"},
			"primary": {Role: "Primary", DiskState: "UpToDate"},
		},
	}
	assert.Equal(t, "roomy", bm.nodeWithSnapshotRoom(context.Background(), info))

	// Thin snapshots need no reservation, so the first Secondary stays first.
	dep.lvIsThinFunc = func(context.Context, string, string, string) (bool, error) { return true, nil }
	assert.Equal(t, "full", bm.nodeWithSnapshotRoom(context.Background(), info))
}
