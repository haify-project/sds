package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestSnapshotScheduleCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	schedule := &SnapshotSchedule{
		Name:     "data",
		Resource: "data",
		Cron:     "0 * * * *",
		Enabled:  true,
		Keep:     GFSPolicy{Hourly: 6, Daily: 7, Weekly: 4},
	}
	require.NoError(t, db.SaveSnapshotSchedule(ctx, schedule))
	assert.False(t, schedule.CreatedAt.IsZero())
	assert.False(t, schedule.UpdatedAt.IsZero())

	got, err := db.GetSnapshotSchedule(ctx, schedule.Name)
	require.NoError(t, err)
	assert.Equal(t, schedule.Keep, got.Keep)

	createdAt := got.CreatedAt
	schedule.Enabled = false
	schedule.LastRun = time.Now().Truncate(time.Second)
	require.NoError(t, db.SaveSnapshotSchedule(ctx, schedule))
	got, err = db.GetSnapshotSchedule(ctx, schedule.Name)
	require.NoError(t, err)
	assert.False(t, got.Enabled)
	assert.Equal(t, createdAt.Unix(), got.CreatedAt.Unix())
	assert.Equal(t, schedule.LastRun.Unix(), got.LastRun.Unix())

	all, err := db.ListSnapshotSchedules(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "data", all[0].Name)

	require.NoError(t, db.DeleteSnapshotSchedule(ctx, schedule.Name))
	_, err = db.GetSnapshotSchedule(ctx, schedule.Name)
	require.Error(t, err)
}

func TestSnapshotScheduleCorruptRecord(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	require.NoError(t, db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(schedulesBucket)).Put([]byte("corrupt"), []byte("not-json"))
	}))
	_, err := db.GetSnapshotSchedule(context.Background(), "corrupt")
	require.Error(t, err)
	_, err = db.ListSnapshotSchedules(context.Background())
	require.Error(t, err)
}
