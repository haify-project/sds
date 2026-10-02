package controller

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/haify-project/sds/pkg/config"
	"github.com/haify-project/sds/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

// A database written by a newer controller must stop this one at start-up.
// New() deliberately tolerates most database failures by running without
// persistence; this is the case where that fallback would itself be the data
// loss, so it is asserted from the outside rather than trusted to stay wired.
func TestNewRefusesToStartOnNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sds.db")

	// Build a valid database, then stamp it with a version from the future.
	// The bucket and key are spelled out rather than imported because they are
	// on-disk names: if they ever change, this test should notice.
	db, err := database.Open(&database.Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	bdb, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.NoError(t, bdb.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("_meta"))
		if err != nil {
			return err
		}
		return b.Put([]byte("schema_version"), []byte("9999"))
	}))
	require.NoError(t, bdb.Close())

	ctrl, err := New(&config.Config{Database: config.DatabaseConfig{Path: path}}, zap.NewNop())
	require.Error(t, err)
	assert.Nil(t, ctrl)
	assert.True(t, database.IsSchemaIncompatible(err))
	assert.Contains(t, err.Error(), "9999")
}
