package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOpenClose(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	logger := zap.NewNop()
	cfg := &Config{Path: dbPath}

	// Open database
	db, err := Open(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, db)

	// Verify file exists
	_, err = os.Stat(dbPath)
	require.NoError(t, err)

	// Close database
	err = db.Close()
	assert.NoError(t, err)
}

func TestOpenCreatesDirectory(t *testing.T) {
	// Create a nested temp path that doesn't exist yet
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "nested", "dir", "test.db")

	logger := zap.NewNop()
	cfg := &Config{Path: dbPath}

	// Open should create the directory
	db, err := Open(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, db)
	// Teardown only. The assertions below read the filesystem, not the handle,
	// and bolt has already fsynced anything Open wrote, so a close failure
	// cannot change what this test observes.
	defer func() { _ = db.Close() }()

	// Verify directory was created
	dir := filepath.Dir(dbPath)
	_, err = os.Stat(dir)
	require.NoError(t, err)
}

// ==================== Node Tests ====================

func TestNodeCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create
	node := &Node{
		Name:     "test-node",
		Address:  "192.168.1.100",
		Hostname: "test-node.local",
		State:    "online",
		Version:  "1.0.0",
		LastSeen: time.Now(),
	}

	err := db.SaveNode(ctx, node)
	require.NoError(t, err)

	// Read
	retrieved, err := db.GetNode(ctx, node.Address)
	require.NoError(t, err)
	assert.Equal(t, node.Name, retrieved.Name)
	assert.Equal(t, node.Address, retrieved.Address)
	assert.Equal(t, node.Hostname, retrieved.Hostname)
	assert.Equal(t, node.State, retrieved.State)
	assert.False(t, retrieved.CreatedAt.IsZero())
	assert.False(t, retrieved.UpdatedAt.IsZero())

	// Update
	node.State = "offline"
	node.Version = "1.0.1"
	err = db.SaveNode(ctx, node)
	require.NoError(t, err)

	retrieved, err = db.GetNode(ctx, node.Address)
	require.NoError(t, err)
	assert.Equal(t, "offline", retrieved.State)
	assert.Equal(t, "1.0.1", retrieved.Version)

	// List
	nodes, err := db.ListNodes(ctx)
	require.NoError(t, err)
	assert.Len(t, nodes, 1)

	// Delete
	err = db.DeleteNode(ctx, node.Address)
	require.NoError(t, err)

	// Verify deleted
	_, err = db.GetNode(ctx, node.Address)
	assert.Error(t, err)
}

func TestGetNodeNotFound(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	_, err := db.GetNode(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestListNodesEmpty(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	nodes, err := db.ListNodes(ctx)
	require.NoError(t, err)
	assert.Empty(t, nodes)
}

// ==================== Pool Tests ====================

func TestPoolCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create
	pool := &Pool{
		Name:    "vg-data",
		Type:    "vg",
		Node:    "node1",
		TotalGB: 1000,
		FreeGB:  500,
		Devices: "/dev/sda,/dev/sdb",
	}

	err := db.SavePool(ctx, pool)
	require.NoError(t, err)

	// Read
	retrieved, err := db.GetPool(ctx, pool.Name)
	require.NoError(t, err)
	assert.Equal(t, pool.Name, retrieved.Name)
	assert.Equal(t, pool.Type, retrieved.Type)
	assert.Equal(t, pool.TotalGB, retrieved.TotalGB)
	assert.Equal(t, pool.FreeGB, retrieved.FreeGB)

	// Update
	pool.FreeGB = 400
	err = db.SavePool(ctx, pool)
	require.NoError(t, err)

	retrieved, err = db.GetPool(ctx, pool.Name)
	require.NoError(t, err)
	assert.Equal(t, 400, retrieved.FreeGB)

	// List
	pools, err := db.ListPools(ctx)
	require.NoError(t, err)
	assert.Len(t, pools, 1)

	// Delete
	err = db.DeletePool(ctx, pool.Name)
	require.NoError(t, err)

	_, err = db.GetPool(ctx, pool.Name)
	assert.Error(t, err)
}

func TestRBACPersistenceCopiesData(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	empty, err := db.LoadRBAC(ctx)
	require.NoError(t, err)
	assert.Nil(t, empty)

	original := []byte(`{"users":["admin"]}`)
	require.NoError(t, db.SaveRBAC(ctx, original))
	original[0] = 'X'

	loaded, err := db.LoadRBAC(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"users":["admin"]}`), loaded)
	loaded[0] = 'Y'

	again, err := db.LoadRBAC(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"users":["admin"]}`), again)
}

// ==================== Helper Functions ====================

func newTestDB(t *testing.T) (*DB, func()) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	logger := zap.NewNop()
	cfg := &Config{Path: dbPath}

	db, err := Open(cfg, logger)
	require.NoError(t, err)

	cleanup := func() {
		// Teardown for a database under t.TempDir(); every assertion has already
		// run against the open handle by the time this fires.
		_ = db.Close()
	}

	return db, cleanup
}
