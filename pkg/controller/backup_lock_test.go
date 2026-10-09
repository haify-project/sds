package controller

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/backup"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
)

var lockFlagsRE = regexp.MustCompile(`--s3-object-lock-mode (\S+) --s3-object-lock-retain-until-date (\S+)`)

// lockedTarget is an object store that honours Object Lock (unless ignore is
// set, like servers that accept the headers and drop them), recording each
// object's lock as the push set it.
type lockedTarget struct {
	locks  map[string][2]string // object -> mode, until
	ignore bool
	cmds   []string
}

func withLockedTarget(t *testing.T, f *incrementalFixture) *lockedTarget {
	t.Helper()
	require.NoError(t, f.ctrl.backups.AddTarget(context.Background(), backup.TargetSpec{
		Name: "offsite", Kind: backup.KindS3, Bucket: "b", User: "k", Secret: "s3cr3t",
		LockMode: backup.LockCompliance, LockDays: 30,
	}))
	lt := &lockedTarget{locks: map[string][2]string{}}
	dep := f.ctrl.deployment.(*fakeDeploymentClient)
	inner := dep.execFunc
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		lt.cmds = append(lt.cmds, cmd)
		switch {
		case strings.Contains(cmd, "rclone version"):
			return successExecResult(hosts, "rclone v1.74.0\n"), nil
		case strings.Contains(cmd, "lsjson --metadata"):
			obj := remoteObject.FindStringSubmatch(cmd)[1]
			l, ok := lt.locks[obj]
			if !ok || lt.ignore {
				return successExecResult(hosts, `[{"Path":"x","Metadata":{}}]`), nil
			}
			return successExecResult(hosts, fmt.Sprintf(
				`[{"Path":"x","Metadata":{"object-lock-mode":%q,"object-lock-retain-until-date":%q}}]`, l[0], l[1])), nil
		case strings.Contains(cmd, "rcat"):
			if m := lockFlagsRE.FindStringSubmatch(cmd); m != nil {
				for _, o := range remoteObject.FindAllStringSubmatch(cmd, -1) {
					lt.locks[o[1]] = [2]string{m[1], m[2]}
				}
			}
		}
		return inner(ctx, hosts, cmd, opts...)
	}
	return lt
}

// Every object of a backup on a locked target is locked until the chain's
// date, and that is checked on the target, not assumed.
func TestBackupToALockedTargetLocksEveryObject(t *testing.T) {
	f := newIncrementalFixture(t)
	lt := withLockedTarget(t, f)

	rec := f.backup(t, false)
	assert.Equal(t, "compliance", rec.LockMode)
	want := rec.StartedAt.AddDate(0, 0, backup.DefaultFullEveryDays+30).UTC().Truncate(time.Second)
	assert.Equal(t, want, rec.RetainUntil, "a new chain is locked for full_every_days + lock_days")

	require.NotEmpty(t, lt.locks)
	for obj, l := range lt.locks {
		assert.Equal(t, "COMPLIANCE", l[0], obj)
		assert.Equal(t, want.Format(time.RFC3339), l[1], obj)
	}
	_, manifestLocked := lt.locks["data/"+rec.ID+"/manifest.json"]
	assert.True(t, manifestLocked, "the manifest, which import relies on, is locked too")

	// The second backup joins the chain and shares its date.
	second := f.backup(t, false)
	assert.Equal(t, database.BackupKindIncremental, second.Kind)
	assert.Equal(t, rec.RetainUntil, second.RetainUntil, "no link may expire before a backup built on it")
}

// A server that accepts the lock headers and stores the object unlocked would
// produce a backup that looks immutable and is not.
func TestBackupFailsWhenTheTargetDoesNotLock(t *testing.T) {
	f := newIncrementalFixture(t)
	lt := withLockedTarget(t, f)
	lt.ignore = true

	_, err := f.ctrl.backups.CreateBackup(context.Background(), "data", "offsite", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not immutable")
	backups, _ := f.ctrl.db.ListBackups(context.Background(), "data", "offsite")
	require.Len(t, backups, 1)
	assert.Equal(t, database.BackupStateFailed, backups[0].State)
}

func TestLockedChainWindow(t *testing.T) {
	spec := backup.TargetSpec{Kind: backup.KindS3, LockMode: backup.LockGovernance, LockDays: 30, FullEveryDays: 7}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	parent := &database.Backup{LockMode: "governance", RetainUntil: chainRetainUntil(spec, nil, start)}
	assert.Equal(t, start.AddDate(0, 0, 37), parent.RetainUntil)

	assert.Empty(t, lockedChainRefusal(spec, parent, start.AddDate(0, 0, 7)), "day 7: the chain still covers 30 days")
	assert.Contains(t, lockedChainRefusal(spec, parent, start.AddDate(0, 0, 7).Add(time.Second)), "new chain")
	assert.Contains(t, lockedChainRefusal(spec, &database.Backup{}, start), "not locked", "an unlocked parent cannot carry a locked chain")
	assert.Equal(t, parent.RetainUntil, chainRetainUntil(spec, parent, start.AddDate(0, 0, 3)))
	assert.Empty(t, lockedChainRefusal(backup.TargetSpec{Kind: backup.KindS3}, &database.Backup{}, start), "unlocked targets have no window")
}

// Neither a person nor retention deletes a locked backup; --force drops only
// the record and never pretends the objects are gone.
func TestLockedBackupsAreNotDeleted(t *testing.T) {
	f := newIncrementalFixture(t)
	lt := withLockedTarget(t, f)
	rec := f.backup(t, false)
	ctx := context.Background()

	err := f.ctrl.backups.DeleteBackup(ctx, rec.ID, "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is locked")

	lt.cmds = nil
	require.NoError(t, f.ctrl.backups.DeleteBackup(ctx, rec.ID, "", true))
	for _, c := range lt.cmds {
		assert.NotContains(t, c, "deletefile", "a delete would only hide the locked objects")
	}
	_, err = f.ctrl.db.GetBackup(ctx, rec.ID)
	assert.Error(t, err, "the record is gone")

	expired := &database.Backup{LockMode: "compliance", RetainUntil: time.Now().Add(-time.Hour)}
	touch, err := assertDeletable(expired, false, time.Now())
	require.NoError(t, err)
	assert.True(t, touch, "after the lock expires it is an ordinary delete")
}

// A restore reads the versions the backup wrote, whatever was written over
// them or deleted since.
func TestRestoreOfALockedBackupReadsItAsWritten(t *testing.T) {
	f := newIncrementalFixture(t)
	lt := withLockedTarget(t, f)
	rec := f.backup(t, false)

	lt.cmds = nil
	_, err := f.ctrl.backups.RestoreBackup(context.Background(), rec.ID, "", "")
	require.NoError(t, err)
	at := rec.FinishedAt.Add(readAtSlack).UTC().Format(time.RFC3339)
	pulled := false
	for _, c := range lt.cmds {
		if strings.Contains(c, "dd of=") && strings.Contains(c, " cat ") {
			pulled = true
			assert.Contains(t, c, "--s3-version-at "+at)
		}
	}
	assert.True(t, pulled)
}

// After the backups were deleted with the target's own keys — on a versioned
// bucket that only hides them — --as-of finds them, and their restores read
// those versions.
func TestImportAsOfReadsTheTargetAsItWas(t *testing.T) {
	f := newIncrementalFixture(t)
	b := withBucket(f)
	rec := f.backup(t, false)
	ctx := context.Background()
	require.NoError(t, f.ctrl.db.DeleteBackup(ctx, rec.ID))

	var listed []string
	dep := f.ctrl.deployment.(*fakeDeploymentClient)
	inner := dep.execFunc
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "lsjson") || strings.Contains(cmd, " cat ") {
			listed = append(listed, cmd)
		}
		return inner(ctx, hosts, cmd, opts...)
	}
	asOf := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	res, err := f.ctrl.backups.ImportBackups(ctx, "offsite", "10.0.0.1", asOf)
	require.NoError(t, err)
	require.Equal(t, []string{rec.ID}, res.Imported)
	require.NotEmpty(t, listed)
	for _, c := range listed {
		assert.Contains(t, c, "--s3-version-at 2026-10-01T12:00:00Z")
	}
	got, err := f.ctrl.db.GetBackup(ctx, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, asOf, got.ReadAt.UTC())
	assert.Equal(t, asOf, readAtFor(got).UTC())
	_ = b
}

// Retention leaves a locked backup for a later run, rather than failing on it
// every time.
func TestPruneLeavesLockedBackups(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	ctx := context.Background()
	sched := &database.BackupSchedule{Name: database.BackupScheduleName("data", "offsite"), Resource: "data",
		Target: "offsite", Keep: database.GFSPolicy{Daily: 1}}
	until := time.Now().Add(240 * time.Hour)
	for i := 0; i < 3; i++ {
		day := time.Now().AddDate(0, 0, -i-1)
		require.NoError(t, ctrl.db.SaveBackup(ctx, &database.Backup{
			ID: fmt.Sprintf("data_%d", i), Resource: "data", Target: "offsite", Schedule: sched.Name,
			State: database.BackupStateCompleted, Kind: database.BackupKindFull, StartedAt: day, FinishedAt: day,
			LockMode: "compliance", RetainUntil: until,
		}))
	}
	ctrl.schedules.pruneBackups(ctx, sched)

	left, err := ctrl.db.ListBackups(ctx, "data", "offsite")
	require.NoError(t, err)
	assert.Len(t, left, 3, "every expired backup is still locked")
	assert.Empty(t, stub.deletes)
}
