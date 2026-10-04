package database

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The shipper reads the trail after a cursor and moves the cursor itself; the
// cursor survives a reopen, which is what a failover is to the database.
func TestAuditShipCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		appendEvents(t, db, &AuditEvent{Method: fmt.Sprintf("M%d", i), Timestamp: time.Now()})
	}
	cur, err := db.AuditShipCursor(ctx, "syslog")
	require.NoError(t, err)
	assert.Zero(t, cur)

	recs, err := db.AuditEventsAfter(ctx, cur, 3)
	require.NoError(t, err)
	require.Len(t, recs, 3)
	assert.Equal(t, []uint64{1, 2, 3}, []uint64{recs[0].Seq, recs[1].Seq, recs[2].Seq})
	assert.Equal(t, "M0", recs[0].Event.Method)
	require.NoError(t, db.SetAuditShipCursor(ctx, "syslog", recs[2].Seq))
	require.NoError(t, db.Close())

	db, err = Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	cur, err = db.AuditShipCursor(ctx, "syslog")
	require.NoError(t, err)
	assert.EqualValues(t, 3, cur)
	recs, err = db.AuditEventsAfter(ctx, cur, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"M3", "M4"}, []string{recs[0].Event.Method, recs[1].Event.Method})
	other, _ := db.AuditShipCursor(ctx, "webhook")
	assert.Zero(t, other, "each destination has its own cursor")
}

// Entries go when their retention runs out, oldest first, and only those.
func TestPruneExpiredAudit(t *testing.T) {
	db, err := Open(&Config{Path: filepath.Join(t.TempDir(), "a.db"), AuditMaxAge: 24 * time.Hour}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	appendEvents(t, db,
		&AuditEvent{Method: "old1", Timestamp: now.Add(-72 * time.Hour)},
		&AuditEvent{Method: "old2", Timestamp: now.Add(-48 * time.Hour)},
		&AuditEvent{Method: "new", Timestamp: now.Add(-time.Hour)})

	n, err := db.PruneExpiredAudit(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "a batch at a time")
	n, err = db.PruneExpiredAudit(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	left, total, err := db.ListAuditEvents(context.Background(), AuditFilter{})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Equal(t, []string{"new"}, methodsOf(left))
}

// The cap still holds whatever the age, and dropping an entry the retention
// period promised to keep is counted, so it can be raised as an event.
func TestAuditCapCountsEntriesDroppedEarly(t *testing.T) {
	db, err := Open(&Config{Path: filepath.Join(t.TempDir(), "a.db"), AuditRetention: 10, AuditMaxAge: 24 * time.Hour},
		zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for i := 0; i < 10+pruneSlack+1; i++ {
		appendEvents(t, db, &AuditEvent{Method: "flood", Timestamp: time.Now()})
	}
	_, total, err := db.ListAuditEvents(context.Background(), AuditFilter{})
	require.NoError(t, err)
	assert.Equal(t, 10, total)
	assert.EqualValues(t, pruneSlack+1, db.TakeAuditTruncated())
	assert.Zero(t, db.TakeAuditTruncated(), "taking resets the count")
}
