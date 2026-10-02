package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/config"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/logbuf"
)

// The log RPCs must be implemented on the type that is actually registered
// with gRPC. Hanging them off *Controller instead compiles, passes review and
// serves "method not implemented" at runtime — a failure that only shows up
// against a deployed binary.
func TestLogRPCsAreServedByTheRegisteredType(t *testing.T) {
	var srv sdspb.SDSControllerServer = NewServer(&Controller{logger: zap.NewNop()})
	require.NotNil(t, srv)

	// A nil-bodied call must reach our implementation, not the embedded
	// Unimplemented stub, which answers with codes.Unimplemented.
	_, err := srv.ListControllerLogs(context.Background(), &sdspb.ListControllerLogsRequest{})
	assert.NoError(t, err)
	_, err = srv.ListAuditEvents(context.Background(), &sdspb.ListAuditEventsRequest{})
	assert.NoError(t, err)
}

func testServer(t *testing.T, ring *logbuf.Ring, db *database.DB, auditOn bool) *Server {
	t.Helper()
	return NewServer(&Controller{
		logger:  zap.NewNop(),
		logRing: ring,
		db:      db,
		config:  &config.Config{Audit: config.AuditConfig{Enabled: auditOn}},
	})
}

func TestListControllerLogsReturnsRecentLines(t *testing.T) {
	ring := logbuf.New(50)
	log := zap.New(ring.Core(zap.NewAtomicLevelAt(zapcore.DebugLevel)))
	log.Info("Evicting HA resource", zap.String("resource", "sds-meta"))
	log.Warn("Starting failed")

	srv := testServer(t, ring, nil, false)
	resp, err := srv.ListControllerLogs(context.Background(), &sdspb.ListControllerLogsRequest{})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)
	require.Len(t, resp.Entries, 2)
	assert.Equal(t, "Starting failed", resp.Entries[0].Message, "newest first")
	assert.Equal(t, "sds-meta", resp.Entries[1].Fields["resource"])
}

func TestListControllerLogsRejectsUnknownLevel(t *testing.T) {
	srv := testServer(t, logbuf.New(10), nil, false)
	resp, err := srv.ListControllerLogs(context.Background(),
		&sdspb.ListControllerLogsRequest{Level: "verbose"})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "verbose")
}

// "No entries" and "nothing is being recorded" look identical in a table and
// mean opposite things, so the disabled case must say so rather than return an
// empty, successful list.
func TestListAuditEventsSaysWhenDisabled(t *testing.T) {
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "a.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	srv := testServer(t, nil, db, false)
	resp, err := srv.ListAuditEvents(context.Background(), &sdspb.ListAuditEventsRequest{})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "disabled")
}

func TestListAuditEventsSaysWhenThereIsNoDatabase(t *testing.T) {
	srv := testServer(t, nil, nil, true)
	resp, err := srv.ListAuditEvents(context.Background(), &sdspb.ListAuditEventsRequest{})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "persistence")
}

func TestListAuditEventsReturnsTrail(t *testing.T) {
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "a.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	require.NoError(t, db.AppendAuditEvent(ctx, &database.AuditEvent{
		Method: "CreatePool", Result: "OK", Granted: true, Node: "sds-b",
	}))
	require.NoError(t, db.AppendAuditEvent(ctx, &database.AuditEvent{
		Method: "EvictHa", Target: "sds-meta", Result: "OK", Granted: true, Node: "sds-e",
	}))

	srv := testServer(t, nil, db, true)
	resp, err := srv.ListAuditEvents(ctx, &sdspb.ListAuditEventsRequest{})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)
	require.Len(t, resp.Events, 2)
	assert.Equal(t, "EvictHa", resp.Events[0].Method, "newest first")
	assert.Equal(t, "sds-meta", resp.Events[0].Target)
	// Which node recorded it matters: the controller relocates, so without it
	// a trail spanning a failover cannot be read.
	assert.Equal(t, "sds-e", resp.Events[0].Node)
	assert.Equal(t, int64(2), resp.Total)
}

// The interceptor's sink must never be able to fail the call it is recording.
func TestAuditSinkSurvivesAClosedDatabase(t *testing.T) {
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "a.db")}, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	ctrl := &Controller{logger: zap.NewNop(), db: db}
	sink := ctrl.auditSink()
	require.NotNil(t, sink)
	assert.NotPanics(t, func() {
		sink(&database.AuditEvent{Method: "EvictHa", Result: "OK"})
	})
}

func TestAuditSinkIsNilWithoutADatabase(t *testing.T) {
	ctrl := &Controller{logger: zap.NewNop()}
	assert.Nil(t, ctrl.auditSink(), "log-only is the correct fallback, not a crash")
}
