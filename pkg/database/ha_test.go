package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ==================== HA Config Tests ====================

func TestHaConfigCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create
	haCfg := &HaConfig{
		Resource:   "ha-resource",
		VIP:        "192.168.1.200/24",
		MountPoint: "/mnt/data",
		FsType:     "ext4",
		Services:   []string{"postgresql.service", "nginx.service"},
	}

	err := db.SaveHaConfig(ctx, haCfg)
	require.NoError(t, err)

	// Read
	retrieved, err := db.GetHaConfig(ctx, haCfg.Resource)
	require.NoError(t, err)
	assert.Equal(t, haCfg.Resource, retrieved.Resource)
	assert.Equal(t, haCfg.VIP, retrieved.VIP)
	assert.Equal(t, haCfg.MountPoint, retrieved.MountPoint)
	assert.Equal(t, haCfg.Services, retrieved.Services)

	// Update
	haCfg.Services = append(haCfg.Services, "redis.service")
	err = db.SaveHaConfig(ctx, haCfg)
	require.NoError(t, err)

	retrieved, err = db.GetHaConfig(ctx, haCfg.Resource)
	require.NoError(t, err)
	assert.Len(t, retrieved.Services, 3)

	// List
	configs, err := db.ListHaConfigs(ctx)
	require.NoError(t, err)
	assert.Len(t, configs, 1)

	// Delete
	err = db.DeleteHaConfig(ctx, haCfg.Resource)
	require.NoError(t, err)

	_, err = db.GetHaConfig(ctx, haCfg.Resource)
	assert.Error(t, err)
}
