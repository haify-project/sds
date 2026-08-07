package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

// Bucket names
const (
	nodesBucket     = "nodes"
	poolsBucket     = "pools"
	resourcesBucket = "resources"
	profilesBucket  = "resource_profiles"
	volumesBucket   = "volumes"
	gatewaysBucket  = "gateways"
	haConfigsBucket = "ha_configs"
	rbacBucket      = "rbac"
	schedulesBucket = "snapshot_schedules"
)

// rbacStateKey is the single key under rbacBucket holding the serialized RBAC
// snapshot (policies, role assignments and token digests).
const rbacStateKey = "state"

// DB holds the database connection
type DB struct {
	db     *bolt.DB
	path   string
	logger *zap.Logger
	mu     sync.RWMutex
	// auditCap bounds the audit trail; zero means DefaultAuditRetention.
	auditCap int
}

// Config holds database configuration
type Config struct {
	Path string // Database file path
	// AuditRetention bounds the number of audit entries kept. Zero selects
	// DefaultAuditRetention.
	AuditRetention int
}

// Default database path
const DefaultDBPath = "/var/lib/sds/sds.db"

// Open opens the database connection
func Open(cfg *Config, logger *zap.Logger) (*DB, error) {
	if cfg == nil {
		cfg = &Config{Path: DefaultDBPath}
	}

	// Ensure directory exists
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database
	db, err := bolt.Open(cfg.Path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Initialize buckets
	if err := db.Update(func(tx *bolt.Tx) error {
		buckets := []string{nodesBucket, poolsBucket, resourcesBucket, profilesBucket, volumesBucket, gatewaysBucket, haConfigsBucket, rbacBucket, schedulesBucket}
		for _, bucket := range buckets {
			_, err := tx.CreateBucketIfNotExists([]byte(bucket))
			if err != nil {
				return fmt.Errorf("failed to create bucket %s: %w", bucket, err)
			}
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize buckets: %w", err)
	}

	database := &DB{
		db:       db,
		path:     cfg.Path,
		logger:   logger,
		auditCap: cfg.AuditRetention,
	}

	logger.Info("Database opened", zap.String("path", cfg.Path))

	return database, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.db.Close()
}

// ==================== NODE ====================

// Node represents a stored node
type Node struct {
	Name    string
	Address string
	// ReplicationAddress is the address DRBD uses for this node in generated
	// .res files. Empty means it shares Address — which is how every node
	// registered before this field existed deserializes, so the single-network
	// setup keeps working untouched.
	ReplicationAddress string
	Hostname           string
	State              string
	LastSeen           time.Time
	Version            string
	// Labels is a JSON-encoded map[string]string of arbitrary node tags (e.g.
	// {"rack":"A","zone":"east"}) used by placement constraints. Empty for nodes
	// registered before labels existed — deserializes as no labels.
	Labels    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SaveNode saves or updates a node
func (db *DB) SaveNode(ctx context.Context, node *Node) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if node.CreatedAt.IsZero() {
		node.CreatedAt = now
	}
	node.UpdatedAt = now

	data, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("failed to marshal node: %w", err)
	}

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(nodesBucket))
		return b.Put([]byte(node.Address), data)
	})
}

// GetNode retrieves a node by address
func (db *DB) GetNode(ctx context.Context, address string) (*Node, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var node Node
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(nodesBucket))
		data := b.Get([]byte(address))
		if data == nil {
			return fmt.Errorf("node not found")
		}
		return json.Unmarshal(data, &node)
	})

	if err != nil {
		return nil, err
	}
	return &node, nil
}

// ListNodes lists all nodes
func (db *DB) ListNodes(ctx context.Context) ([]*Node, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var nodes []*Node
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(nodesBucket))
		return b.ForEach(func(k, v []byte) error {
			var node Node
			if err := json.Unmarshal(v, &node); err != nil {
				return err
			}
			nodes = append(nodes, &node)
			return nil
		})
	})

	return nodes, err
}

// DeleteNode deletes a node by address
func (db *DB) DeleteNode(ctx context.Context, address string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(nodesBucket))
		return b.Delete([]byte(address))
	})
}

// ==================== POOL ====================

// Pool represents a storage pool
type Pool struct {
	Name      string
	Type      string
	Node      string
	TotalGB   int
	FreeGB    int
	Devices   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SavePool saves or updates a pool
func (db *DB) SavePool(ctx context.Context, pool *Pool) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if pool.CreatedAt.IsZero() {
		pool.CreatedAt = now
	}
	pool.UpdatedAt = now

	data, err := json.Marshal(pool)
	if err != nil {
		return fmt.Errorf("failed to marshal pool: %w", err)
	}

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(poolsBucket))
		return b.Put([]byte(pool.Name), data)
	})
}

// GetPool retrieves a pool by name
func (db *DB) GetPool(ctx context.Context, name string) (*Pool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var pool Pool
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(poolsBucket))
		data := b.Get([]byte(name))
		if data == nil {
			return fmt.Errorf("pool not found")
		}
		return json.Unmarshal(data, &pool)
	})

	if err != nil {
		return nil, err
	}
	return &pool, nil
}

// ListPools lists all pools
func (db *DB) ListPools(ctx context.Context) ([]*Pool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var pools []*Pool
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(poolsBucket))
		return b.ForEach(func(k, v []byte) error {
			var pool Pool
			if err := json.Unmarshal(v, &pool); err != nil {
				return err
			}
			pools = append(pools, &pool)
			return nil
		})
	})

	return pools, err
}

// DeletePool deletes a pool by name
func (db *DB) DeletePool(ctx context.Context, name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(poolsBucket))
		return b.Delete([]byte(name))
	})
}

// ==================== RESOURCE ====================

// Resource represents a DRBD resource
type Resource struct {
	Name     string
	Port     int
	Nodes    string
	Protocol string
	Replicas int
	// Profile records the creation profile for attribution only. Existing
	// resources do not depend on the profile continuing to exist.
	Profile string
	// Labels are arbitrary resource metadata used for organization and search.
	Labels map[string]string
	// DisklessNodes is a comma-separated list of node names that participate
	// in the resource as diskless quorum tiebreakers (they vote in quorum but
	// store no data). Empty for ordinary all-diskful resources.
	DisklessNodes string
	// DisklessClients is a comma-separated list of node names attached to the
	// resource as diskless data clients: they carry no local replica but connect
	// over DRBD and can be promoted Primary to read/write the volume over the
	// network (LINSTOR's "diskless client"). Distinct from DisklessNodes, which
	// are quorum-only tiebreakers that must never be promoted or mounted. Empty
	// for resources with no diskless clients.
	DisklessClients string
	// WAN replication (opt-in). All zero-valued for an ordinary LAN resource, so
	// existing records deserialize as LAN and every WAN code path stays gated
	// behind WANMode. See docs/2026-07-05-wan-replication-design.md.
	WANMode    bool   // false = LAN (default); true routes DRBD via a per-resource sds-proxy pair
	DRNode     string // the DR-site node name (WAN only)
	DREndpoint string // the DR site's public WAN address the primary dials (WAN only)
	WANPort    int    // WAN mTLS port the DR acceptor listens on (WAN only)
	// WANEgressAddress optionally pins the source address the primary's proxy
	// binds before dialing the DR site (WAN only). Empty = routing table decides.
	// Persisted because the proxy config is re-rendered from this record, so a
	// controller restart would otherwise silently drop the pinned egress.
	WANEgressAddress string
	// Encrypted records that every replica's backing volume is a LUKS2
	// container, so DRBD consumes /dev/mapper/<container> instead of the LV or
	// zvol directly. False for every record written before this existed, which
	// is exactly right: those resources are not encrypted.
	//
	// No key material is stored here, or anywhere else in the controller. Each
	// node generates and keeps its own key; see pkg/controller/encryption.go.
	Encrypted bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ResourceProfile contains defaults applied when a resource is created.
type ResourceProfile struct {
	Name        string
	Protocol    string
	StorageType string
	Pool        string
	Replicas    int
	OnDifferent []string
	OnSame      []string
	DRBDOptions map[string]string
	Labels      map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SaveResourceProfile saves or updates a resource profile.
func (db *DB) SaveResourceProfile(ctx context.Context, profile *ResourceProfile) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = now
	}
	profile.UpdatedAt = now

	data, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("failed to marshal resource profile: %w", err)
	}

	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(profilesBucket)).Put([]byte(profile.Name), data)
	})
}

// GetResourceProfile retrieves a resource profile by name.
func (db *DB) GetResourceProfile(ctx context.Context, name string) (*ResourceProfile, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var profile ResourceProfile
	err := db.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(profilesBucket)).Get([]byte(name))
		if data == nil {
			return fmt.Errorf("resource profile not found")
		}
		return json.Unmarshal(data, &profile)
	})
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

// ListResourceProfiles lists all resource profiles.
func (db *DB) ListResourceProfiles(ctx context.Context) ([]*ResourceProfile, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	profiles := make([]*ResourceProfile, 0)
	err := db.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(profilesBucket)).ForEach(func(_, value []byte) error {
			var profile ResourceProfile
			if err := json.Unmarshal(value, &profile); err != nil {
				return err
			}
			profiles = append(profiles, &profile)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return profiles, nil
}

// DeleteResourceProfile deletes a resource profile by name.
func (db *DB) DeleteResourceProfile(ctx context.Context, name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(profilesBucket)).Delete([]byte(name))
	})
}

// SaveResource saves or updates a resource
func (db *DB) SaveResource(ctx context.Context, resource *Resource) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if resource.CreatedAt.IsZero() {
		resource.CreatedAt = now
	}
	resource.UpdatedAt = now

	data, err := json.Marshal(resource)
	if err != nil {
		return fmt.Errorf("failed to marshal resource: %w", err)
	}

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(resourcesBucket))
		return b.Put([]byte(resource.Name), data)
	})
}

// GetResource retrieves a resource by name
func (db *DB) GetResource(ctx context.Context, name string) (*Resource, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var resource Resource
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(resourcesBucket))
		data := b.Get([]byte(name))
		if data == nil {
			return fmt.Errorf("resource not found")
		}
		return json.Unmarshal(data, &resource)
	})

	if err != nil {
		return nil, err
	}
	return &resource, nil
}

// ListResources lists all resources
func (db *DB) ListResources(ctx context.Context) ([]*Resource, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var resources []*Resource
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(resourcesBucket))
		return b.ForEach(func(k, v []byte) error {
			var resource Resource
			if err := json.Unmarshal(v, &resource); err != nil {
				return err
			}
			resources = append(resources, &resource)
			return nil
		})
	})

	return resources, err
}

// DeleteResource deletes a resource by name
func (db *DB) DeleteResource(ctx context.Context, name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(resourcesBucket))
		return b.Delete([]byte(name))
	})
}

// ==================== HA CONFIG ====================

// HaConfig represents a highly available configuration
type HaConfig struct {
	Resource   string
	VIP        string
	MountPoint string
	FsType     string
	Services   []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SaveHaConfig saves or updates an HA configuration
func (db *DB) SaveHaConfig(ctx context.Context, cfg *HaConfig) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if cfg.CreatedAt.IsZero() {
		cfg.CreatedAt = now
	}
	cfg.UpdatedAt = now

	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal ha config: %w", err)
	}

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(haConfigsBucket))
		return b.Put([]byte(cfg.Resource), data)
	})
}

// GetHaConfig retrieves an HA configuration by resource name
func (db *DB) GetHaConfig(ctx context.Context, resource string) (*HaConfig, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var cfg HaConfig
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(haConfigsBucket))
		data := b.Get([]byte(resource))
		if data == nil {
			return fmt.Errorf("ha config not found")
		}
		return json.Unmarshal(data, &cfg)
	})

	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ListHaConfigs lists all HA configurations
func (db *DB) ListHaConfigs(ctx context.Context) ([]*HaConfig, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var configs []*HaConfig
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(haConfigsBucket))
		return b.ForEach(func(k, v []byte) error {
			var cfg HaConfig
			if err := json.Unmarshal(v, &cfg); err != nil {
				return err
			}
			configs = append(configs, &cfg)
			return nil
		})
	})

	return configs, err
}

// DeleteHaConfig deletes an HA configuration by resource name
func (db *DB) DeleteHaConfig(ctx context.Context, resource string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(haConfigsBucket))
		return b.Delete([]byte(resource))
	})
}

// ==================== SNAPSHOT SCHEDULE ====================

// GFSPolicy is a grandfather-father-son retention policy: how many snapshots
// to keep in each time bucket. The most recent snapshot in each distinct
// hour/day/week/month/year is retained, up to the count for that bucket; a
// snapshot kept by any bucket survives. Zero in a field disables that bucket.
type GFSPolicy struct {
	Hourly  int
	Daily   int
	Weekly  int
	Monthly int
	Yearly  int
}

// SnapshotSchedule is a cron-driven snapshot policy for one resource. The
// active controller snapshots the resource's volumes on every diskful node on
// each cron tick, then prunes old scheduled snapshots per Keep. One schedule
// per resource (Name is the resource name).
type SnapshotSchedule struct {
	Name      string // unique; equals the target resource name
	Resource  string
	Cron      string
	Enabled   bool
	Keep      GFSPolicy
	LastRun   time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SaveSnapshotSchedule saves or updates a snapshot schedule
func (db *DB) SaveSnapshotSchedule(ctx context.Context, s *SnapshotSchedule) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot schedule: %w", err)
	}

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(schedulesBucket))
		return b.Put([]byte(s.Name), data)
	})
}

// GetSnapshotSchedule retrieves a snapshot schedule by name
func (db *DB) GetSnapshotSchedule(ctx context.Context, name string) (*SnapshotSchedule, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var s SnapshotSchedule
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(schedulesBucket))
		data := b.Get([]byte(name))
		if data == nil {
			return fmt.Errorf("snapshot schedule not found")
		}
		return json.Unmarshal(data, &s)
	})

	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSnapshotSchedules lists all snapshot schedules
func (db *DB) ListSnapshotSchedules(ctx context.Context) ([]*SnapshotSchedule, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var schedules []*SnapshotSchedule
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(schedulesBucket))
		return b.ForEach(func(k, v []byte) error {
			var s SnapshotSchedule
			if err := json.Unmarshal(v, &s); err != nil {
				return err
			}
			schedules = append(schedules, &s)
			return nil
		})
	})

	return schedules, err
}

// DeleteSnapshotSchedule deletes a snapshot schedule by name
func (db *DB) DeleteSnapshotSchedule(ctx context.Context, name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(schedulesBucket))
		return b.Delete([]byte(name))
	})
}

// ==================== GATEWAY ====================

// GatewayType represents the gateway type
type GatewayType string

const (
	GatewayTypeNFS    GatewayType = "nfs"
	GatewayTypeISCSI  GatewayType = "iscsi"
	GatewayTypeNVMEOF GatewayType = "nvmeof"
)

// Gateway represents a storage gateway
type Gateway struct {
	ID         int64
	Name       string
	Resource   string
	Type       GatewayType
	Config     map[string]interface{}
	Status     string
	ActiveNode string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SaveGateway saves or updates a gateway
func (db *DB) SaveGateway(ctx context.Context, gateway *Gateway) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if gateway.CreatedAt.IsZero() {
		gateway.CreatedAt = now
		gateway.ID = time.Now().UnixNano()
	}
	gateway.UpdatedAt = now

	data, err := json.Marshal(gateway)
	if err != nil {
		return fmt.Errorf("failed to marshal gateway: %w", err)
	}

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(gatewaysBucket))
		return b.Put([]byte(gateway.Name), data)
	})
}

// GetGateway retrieves a gateway by name
func (db *DB) GetGateway(ctx context.Context, name string) (*Gateway, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var gateway Gateway
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(gatewaysBucket))
		data := b.Get([]byte(name))
		if data == nil {
			return fmt.Errorf("gateway not found")
		}
		return json.Unmarshal(data, &gateway)
	})

	if err != nil {
		return nil, err
	}
	return &gateway, nil
}

// GetGatewayByResource retrieves a gateway by resource name.
func (db *DB) GetGatewayByResource(ctx context.Context, resource string) (*Gateway, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var result *Gateway
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(gatewaysBucket))
		return b.ForEach(func(k, v []byte) error {
			var gateway Gateway
			if err := json.Unmarshal(v, &gateway); err != nil {
				return err
			}
			if gateway.Resource != resource && gateway.Name != resource {
				return nil
			}
			copyGateway := gateway
			result = &copyGateway
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("gateway not found")
	}
	return result, nil
}

// ListGateways lists all gateways
func (db *DB) ListGateways(ctx context.Context) ([]*Gateway, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var gateways []*Gateway
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(gatewaysBucket))
		return b.ForEach(func(k, v []byte) error {
			var gateway Gateway
			if err := json.Unmarshal(v, &gateway); err != nil {
				return err
			}
			gateways = append(gateways, &gateway)
			return nil
		})
	})

	return gateways, err
}

// DeleteGateway deletes a gateway by name
func (db *DB) DeleteGateway(ctx context.Context, name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(gatewaysBucket))
		return b.Delete([]byte(name))
	})
}

// DeleteGatewayByResource deletes all gateways matching the resource name.
func (db *DB) DeleteGatewayByResource(ctx context.Context, resource string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(gatewaysBucket))
		var keys [][]byte
		err := b.ForEach(func(k, v []byte) error {
			var gateway Gateway
			if err := json.Unmarshal(v, &gateway); err != nil {
				return err
			}
			if gateway.Resource == resource || gateway.Name == resource {
				keys = append(keys, append([]byte(nil), k...))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err := b.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
}

// ==================== VOLUME ====================

// Volume represents a volume in a resource
type Volume struct {
	ID           int64
	ResourceName string
	VolumeName   string
	VolumeID     int
	Pool         string
	SizeGB       int
	Device       string
	CreatedAt    time.Time
}

// SaveVolume saves or updates a volume
func (db *DB) SaveVolume(ctx context.Context, volume *Volume) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if volume.CreatedAt.IsZero() {
		volume.CreatedAt = time.Now()
	}
	if volume.ID == 0 {
		volume.ID = time.Now().UnixNano()
	}

	data, err := json.Marshal(volume)
	if err != nil {
		return fmt.Errorf("failed to marshal volume: %w", err)
	}

	key := fmt.Sprintf("%s:%s", volume.ResourceName, volume.VolumeName)
	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(volumesBucket))
		return b.Put([]byte(key), data)
	})
}

// ListVolumes lists all volumes for a resource
func (db *DB) ListVolumes(ctx context.Context, resourceName string) ([]*Volume, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var volumes []*Volume
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(volumesBucket))
		c := b.Cursor()
		prefix := []byte(resourceName)
		for k, v := c.Seek(prefix); k != nil && len(k) >= len(prefix) && string(k[:len(prefix)]) == resourceName; k, v = c.Next() {
			var volume Volume
			if err := json.Unmarshal(v, &volume); err != nil {
				return err
			}
			volumes = append(volumes, &volume)
		}
		return nil
	})

	return volumes, err
}

// DeleteVolume deletes a volume
func (db *DB) DeleteVolume(ctx context.Context, resourceName, volumeName string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	key := fmt.Sprintf("%s:%s", resourceName, volumeName)
	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(volumesBucket))
		return b.Delete([]byte(key))
	})
}

// ==================== RBAC ====================

// SaveRBAC persists the serialized RBAC snapshot (opaque to the database; the
// rbac package owns the encoding).
func (db *DB) SaveRBAC(ctx context.Context, data []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	// Copy: bbolt does not retain the caller's slice past the transaction.
	buf := make([]byte, len(data))
	copy(buf, data)
	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(rbacBucket)).Put([]byte(rbacStateKey), buf)
	})
}

// LoadRBAC returns the stored RBAC snapshot, or nil if none has been saved yet.
func (db *DB) LoadRBAC(ctx context.Context) ([]byte, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var out []byte
	err := db.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(rbacBucket)).Get([]byte(rbacStateKey))
		if v != nil {
			out = make([]byte, len(v))
			copy(out, v)
		}
		return nil
	})
	return out, err
}
