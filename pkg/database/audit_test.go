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

func auditDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(&Config{Path: filepath.Join(t.TempDir(), "audit.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func appendEvents(t *testing.T, db *DB, evs ...*AuditEvent) {
	t.Helper()
	for _, ev := range evs {
		require.NoError(t, db.AppendAuditEvent(context.Background(), ev))
	}
}

func methodsOf(events []*AuditEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Method)
	}
	return out
}

// An empty trail is the normal state of a fresh controller, not a failure —
// and the bucket does not exist until the first call is recorded.
func TestListAuditEventsOnEmptyTrail(t *testing.T) {
	db := auditDB(t)
	events, total, err := db.ListAuditEvents(context.Background(), AuditFilter{})
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Zero(t, total)
}

// Newest first: the entry you want is nearly always the last thing that
// happened.
func TestListAuditEventsNewestFirst(t *testing.T) {
	db := auditDB(t)
	appendEvents(t, db,
		&AuditEvent{Method: "CreatePool", Result: "OK"},
		&AuditEvent{Method: "CreateHa", Result: "OK"},
		&AuditEvent{Method: "EvictHa", Result: "OK"},
	)

	events, total, err := db.ListAuditEvents(context.Background(), AuditFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{"EvictHa", "CreateHa", "CreatePool"}, methodsOf(events))
	assert.Equal(t, 3, total)
}

// Ordering must hold past the point where a naive decimal key would sort
// "10" before "9". The keys are big-endian sequence numbers for this reason.
func TestListAuditEventsOrderingBeyondTenEntries(t *testing.T) {
	db := auditDB(t)
	for i := 0; i < 15; i++ {
		appendEvents(t, db, &AuditEvent{Method: fmt.Sprintf("Call%02d", i), Result: "OK"})
	}

	events, _, err := db.ListAuditEvents(context.Background(), AuditFilter{Limit: 3})
	require.NoError(t, err)
	assert.Equal(t, []string{"Call14", "Call13", "Call12"}, methodsOf(events))
}

func TestAuditFilters(t *testing.T) {
	db := auditDB(t)
	old := time.Now().Add(-2 * time.Hour)
	appendEvents(t, db,
		&AuditEvent{Method: "EvictHa", Target: "haify-meta", User: "alice", Result: "OK", Timestamp: old},
		&AuditEvent{Method: "DeletePool", Target: "vg0", User: "bob", Result: "PermissionDenied", Timestamp: time.Now()},
		&AuditEvent{Method: "EvictHa", Target: "openclaw", User: "alice", Result: "OK", Timestamp: time.Now()},
	)
	ctx := context.Background()

	t.Run("by method", func(t *testing.T) {
		events, _, err := db.ListAuditEvents(ctx, AuditFilter{Method: "EvictHa"})
		require.NoError(t, err)
		assert.Len(t, events, 2)
	})

	t.Run("by target", func(t *testing.T) {
		events, _, err := db.ListAuditEvents(ctx, AuditFilter{Target: "openclaw"})
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "EvictHa", events[0].Method)
	})

	t.Run("by user", func(t *testing.T) {
		events, _, err := db.ListAuditEvents(ctx, AuditFilter{User: "bob"})
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "DeletePool", events[0].Method)
	})

	// "Show me what went wrong" is the single most common thing to ask of an
	// audit trail, and a denied call is exactly what it exists to record.
	t.Run("failures only", func(t *testing.T) {
		events, _, err := db.ListAuditEvents(ctx, AuditFilter{FailuresOnly: true})
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "PermissionDenied", events[0].Result)
	})

	t.Run("since", func(t *testing.T) {
		events, _, err := db.ListAuditEvents(ctx, AuditFilter{Since: time.Now().Add(-time.Hour)})
		require.NoError(t, err)
		assert.Len(t, events, 2, "the two-hour-old entry is outside the window")
	})
}

// The trail shares a small replicated volume with the rest of the controller's
// state, so unbounded growth is not an option. Pruning must drop the oldest.
//
// The cap is lowered here rather than writing twenty thousand entries: each
// append is a durable commit, so the realistic retention takes minutes to
// reach and proves nothing the small cap does not.
func TestRetentionPrunesOldestFirst(t *testing.T) {
	const retention = 50
	db, err := Open(&Config{
		Path:           filepath.Join(t.TempDir(), "audit.db"),
		AuditRetention: retention,
	}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	const over = retention + pruneSlack + 10
	for i := 0; i < over; i++ {
		appendEvents(t, db, &AuditEvent{Method: fmt.Sprintf("Call%06d", i), Result: "OK"})
	}

	events, total, err := db.ListAuditEvents(context.Background(), AuditFilter{Limit: 1})
	require.NoError(t, err)
	assert.LessOrEqual(t, total, retention+pruneSlack,
		"the trail must not grow without bound")
	require.Len(t, events, 1)
	assert.Equal(t, fmt.Sprintf("Call%06d", over-1), events[0].Method,
		"pruning must drop the oldest entries, never the newest")

	// And the survivors must be the newest ones, not an arbitrary window.
	all, _, err := db.ListAuditEvents(context.Background(), AuditFilter{Limit: retention + pruneSlack})
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("Call%06d", over-len(all)), all[len(all)-1].Method,
		"the oldest surviving entry must be contiguous with the newest")
}

// Fields have to survive the round trip: a trail that loses the client address
// or the latency answers none of the questions it is for.
func TestAuditEventRoundTrip(t *testing.T) {
	db := auditDB(t)
	now := time.Now().Truncate(time.Millisecond)
	appendEvents(t, db, &AuditEvent{
		Timestamp: now,
		Method:    "EvictHa",
		Client:    "175.152.6.49",
		User:      "alice",
		Target:    "haify-meta",
		Result:    "OK",
		Granted:   true,
		Latency:   1044 * time.Millisecond,
		Node:      "haify-e",
	})

	events, _, err := db.ListAuditEvents(context.Background(), AuditFilter{})
	require.NoError(t, err)
	require.Len(t, events, 1)
	got := events[0]
	assert.True(t, got.Timestamp.Equal(now))
	assert.Equal(t, "175.152.6.49", got.Client)
	assert.Equal(t, "alice", got.User)
	assert.Equal(t, "haify-meta", got.Target)
	assert.True(t, got.Granted)
	assert.Equal(t, 1044*time.Millisecond, got.Latency)
	assert.Equal(t, "haify-e", got.Node)
}
