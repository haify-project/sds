package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
)

// Replacing a schedule with one that keeps less makes its next run prune the
// rest: without approval, one token could empty the snapshot and backup
// history. Creating one, or keeping as much, needs nobody.
func TestApprovalHoldsBackShorterRetention(t *testing.T) {
	srv, _ := approvalFixture(t)
	g := srv.ctrl.approvals
	ctx := context.Background()
	ran := 0

	snap := func(daily int32) *sdspb.CreateSnapshotScheduleRequest {
		return &sdspb.CreateSnapshotScheduleRequest{Resource: "db", Cron: "0 * * * *",
			Keep: &sdspb.GFSRetention{Hourly: 24, Daily: daily}}
	}
	require.NoError(t, callThrough(g, as("alice"), "CreateSnapshotSchedule", snap(7), &ran), "a new schedule deletes nothing")
	require.NoError(t, srv.ctrl.db.SaveSnapshotSchedule(ctx, &database.SnapshotSchedule{
		Name: "db", Resource: "db", Keep: database.GFSPolicy{Hourly: 24, Daily: 7}}))
	require.NoError(t, callThrough(g, as("alice"), "CreateSnapshotSchedule", snap(30), &ran), "keeping more deletes nothing")
	pendingID(t, callThrough(g, as("alice"), "CreateSnapshotSchedule", snap(1), &ran))

	bk := func(weekly int32) *sdspb.CreateBackupScheduleRequest {
		return &sdspb.CreateBackupScheduleRequest{Resource: "db", Target: "offsite", Cron: "0 2 * * *",
			Keep: &sdspb.GFSRetention{Weekly: weekly}}
	}
	require.NoError(t, callThrough(g, as("alice"), "CreateBackupSchedule", bk(4), &ran))
	require.NoError(t, srv.ctrl.db.SaveBackupSchedule(ctx, &database.BackupSchedule{
		Name: database.BackupScheduleName("db", "offsite"), Resource: "db", Target: "offsite",
		Keep: database.GFSPolicy{Weekly: 4}}))
	require.NoError(t, callThrough(g, as("alice"), "CreateBackupSchedule", bk(4), &ran))
	pendingID(t, callThrough(g, as("alice"), "CreateBackupSchedule", bk(1), &ran))
	assert.Equal(t, 4, ran, "only the two that keep less were held back")
}
