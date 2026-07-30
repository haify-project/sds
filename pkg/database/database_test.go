package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
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
	defer db.Close()

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

// ==================== Resource Tests ====================

func TestResourceCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create
	resource := &Resource{
		Name:     "data",
		Port:     7000,
		Nodes:    "node1,node2",
		Protocol: "C",
		Replicas: 2,
		Profile:  "production",
		Labels: map[string]string{
			"app": "postgres",
			"env": "prod",
		},
	}

	err := db.SaveResource(ctx, resource)
	require.NoError(t, err)

	// Read
	retrieved, err := db.GetResource(ctx, resource.Name)
	require.NoError(t, err)
	assert.Equal(t, resource.Name, retrieved.Name)
	assert.Equal(t, resource.Port, retrieved.Port)
	assert.Equal(t, resource.Nodes, retrieved.Nodes)
	assert.Equal(t, resource.Protocol, retrieved.Protocol)
	assert.Equal(t, resource.Replicas, retrieved.Replicas)
	assert.Equal(t, resource.Profile, retrieved.Profile)
	assert.Equal(t, resource.Labels, retrieved.Labels)

	// Update
	resource.Replicas = 3
	err = db.SaveResource(ctx, resource)
	require.NoError(t, err)

	retrieved, err = db.GetResource(ctx, resource.Name)
	require.NoError(t, err)
	assert.Equal(t, 3, retrieved.Replicas)

	// List
	resources, err := db.ListResources(ctx)
	require.NoError(t, err)
	assert.Len(t, resources, 1)

	// Delete
	err = db.DeleteResource(ctx, resource.Name)
	require.NoError(t, err)

	_, err = db.GetResource(ctx, resource.Name)
	assert.Error(t, err)
}

func TestResourceProfileCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	profile := &ResourceProfile{
		Name:        "production",
		Protocol:    "C",
		StorageType: "lvm-thin",
		Pool:        "fast",
		Replicas:    3,
		OnDifferent: []string{"zone", "rack"},
		OnSame:      []string{"region"},
		DRBDOptions: map[string]string{"net/max-buffers": "8000"},
		Labels:      map[string]string{"env": "prod"},
	}

	require.NoError(t, db.SaveResourceProfile(ctx, profile))
	retrieved, err := db.GetResourceProfile(ctx, profile.Name)
	require.NoError(t, err)
	assert.Equal(t, profile.Name, retrieved.Name)
	assert.Equal(t, profile.Protocol, retrieved.Protocol)
	assert.Equal(t, profile.StorageType, retrieved.StorageType)
	assert.Equal(t, profile.Pool, retrieved.Pool)
	assert.Equal(t, profile.Replicas, retrieved.Replicas)
	assert.Equal(t, profile.OnDifferent, retrieved.OnDifferent)
	assert.Equal(t, profile.OnSame, retrieved.OnSame)
	assert.Equal(t, profile.DRBDOptions, retrieved.DRBDOptions)
	assert.Equal(t, profile.Labels, retrieved.Labels)
	assert.False(t, retrieved.CreatedAt.IsZero())
	assert.False(t, retrieved.UpdatedAt.IsZero())

	profile.Replicas = 2
	profile.Labels["tier"] = "critical"
	require.NoError(t, db.SaveResourceProfile(ctx, profile))
	retrieved, err = db.GetResourceProfile(ctx, profile.Name)
	require.NoError(t, err)
	assert.Equal(t, 2, retrieved.Replicas)
	assert.Equal(t, "critical", retrieved.Labels["tier"])

	profiles, err := db.ListResourceProfiles(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.Equal(t, profile.Name, profiles[0].Name)

	require.NoError(t, db.DeleteResourceProfile(ctx, profile.Name))
	_, err = db.GetResourceProfile(ctx, profile.Name)
	assert.Error(t, err)
}

func TestResourceProfileErrors(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(profilesBucket)).Put([]byte("corrupt"), []byte("{"))
	}))
	_, err := db.GetResourceProfile(ctx, "corrupt")
	require.Error(t, err)
	_, err = db.ListResourceProfiles(ctx)
	require.Error(t, err)
}

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

// ==================== Gateway Tests ====================

func TestGatewayCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create
	gateway := &Gateway{
		Name:       "nfs-gateway",
		Resource:   "data",
		Type:       GatewayTypeNFS,
		Config:     map[string]interface{}{"export_path": "/export/data"},
		Status:     "active",
		ActiveNode: "node1",
	}

	err := db.SaveGateway(ctx, gateway)
	require.NoError(t, err)
	assert.NotZero(t, gateway.ID)

	// Read
	retrieved, err := db.GetGateway(ctx, gateway.Name)
	require.NoError(t, err)
	assert.Equal(t, gateway.Name, retrieved.Name)
	assert.Equal(t, gateway.Resource, retrieved.Resource)
	assert.Equal(t, gateway.Type, retrieved.Type)
	assert.Equal(t, gateway.Status, retrieved.Status)

	// Update
	gateway.Status = "failed"
	gateway.ActiveNode = "node2"
	err = db.SaveGateway(ctx, gateway)
	require.NoError(t, err)

	retrieved, err = db.GetGateway(ctx, gateway.Name)
	require.NoError(t, err)
	assert.Equal(t, "failed", retrieved.Status)
	assert.Equal(t, "node2", retrieved.ActiveNode)

	// List
	gateways, err := db.ListGateways(ctx)
	require.NoError(t, err)
	assert.Len(t, gateways, 1)

	// Delete
	err = db.DeleteGateway(ctx, gateway.Name)
	require.NoError(t, err)

	_, err = db.GetGateway(ctx, gateway.Name)
	assert.Error(t, err)
}

func TestGatewayTypes(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	types := []GatewayType{GatewayTypeNFS, GatewayTypeISCSI, GatewayTypeNVMEOF}
	for _, gwType := range types {
		gateway := &Gateway{
			Name:     string(gwType) + "-gateway",
			Resource: "data",
			Type:     gwType,
			Status:   "active",
		}

		err := db.SaveGateway(ctx, gateway)
		require.NoError(t, err)
	}

	gateways, err := db.ListGateways(ctx)
	require.NoError(t, err)
	assert.Len(t, gateways, 3)
}

func TestGetGatewayByResource(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	gateway := &Gateway{
		Name:       "data-nfs",
		Resource:   "data",
		Type:       GatewayTypeNFS,
		Status:     "configured",
		ActiveNode: "node1",
	}

	err := db.SaveGateway(ctx, gateway)
	require.NoError(t, err)

	retrieved, err := db.GetGatewayByResource(ctx, "data")
	require.NoError(t, err)
	assert.Equal(t, gateway.Name, retrieved.Name)
	assert.Equal(t, gateway.Resource, retrieved.Resource)
}

func TestDeleteGatewayByResource(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	first := &Gateway{Name: "data-nfs", Resource: "data", Type: GatewayTypeNFS}
	second := &Gateway{Name: "logs-nfs", Resource: "logs", Type: GatewayTypeNFS}

	require.NoError(t, db.SaveGateway(ctx, first))
	require.NoError(t, db.SaveGateway(ctx, second))

	require.NoError(t, db.DeleteGatewayByResource(ctx, "data"))

	_, err := db.GetGateway(ctx, "data-nfs")
	assert.Error(t, err)

	retrieved, err := db.GetGateway(ctx, "logs-nfs")
	require.NoError(t, err)
	assert.Equal(t, "logs", retrieved.Resource)
}

// ==================== Volume Tests ====================

func TestVolumeCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create
	volume := &Volume{
		ResourceName: "data",
		VolumeName:   "vol0",
		VolumeID:     0,
		Pool:         "vg-data",
		SizeGB:       100,
		Device:       "/dev/drbd0",
	}

	err := db.SaveVolume(ctx, volume)
	require.NoError(t, err)
	assert.NotZero(t, volume.ID)

	// List by resource
	volumes, err := db.ListVolumes(ctx, "data")
	require.NoError(t, err)
	assert.Len(t, volumes, 1)
	assert.Equal(t, volume.ResourceName, volumes[0].ResourceName)
	assert.Equal(t, volume.VolumeName, volumes[0].VolumeName)

	// Delete
	err = db.DeleteVolume(ctx, "data", "vol0")
	require.NoError(t, err)

	volumes, err = db.ListVolumes(ctx, "data")
	require.NoError(t, err)
	assert.Empty(t, volumes)
}

func TestListVolumesByResource(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create volumes for different resources
	for i := 0; i < 3; i++ {
		volume := &Volume{
			ResourceName: "resource1",
			VolumeName:   "vol" + string(rune('0'+i)),
			VolumeID:     i,
			Pool:         "vg-data",
			SizeGB:       100 * (i + 1),
			Device:       "/dev/drbd" + string(rune('0'+i)),
		}
		err := db.SaveVolume(ctx, volume)
		require.NoError(t, err)
	}

	for i := 0; i < 2; i++ {
		volume := &Volume{
			ResourceName: "resource2",
			VolumeName:   "vol" + string(rune('0'+i)),
			VolumeID:     i,
			Pool:         "vg-data",
			SizeGB:       50 * (i + 1),
			Device:       "/dev/drbd10" + string(rune('0'+i)),
		}
		err := db.SaveVolume(ctx, volume)
		require.NoError(t, err)
	}

	// List volumes for resource1
	volumes1, err := db.ListVolumes(ctx, "resource1")
	require.NoError(t, err)
	assert.Len(t, volumes1, 3)

	// List volumes for resource2
	volumes2, err := db.ListVolumes(ctx, "resource2")
	require.NoError(t, err)
	assert.Len(t, volumes2, 2)

	// List volumes for nonexistent resource
	volumes3, err := db.ListVolumes(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Empty(t, volumes3)
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
		db.Close()
	}

	return db, cleanup
}
