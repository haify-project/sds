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
	nodesBucket    = "nodes"
	poolsBucket    = "pools"
	resourcesBucket = "resources"
	volumesBucket   = "volumes"
	gatewaysBucket  = "gateways"
)

// DB holds the database connection
type DB struct {
	db     *bolt.DB
	path   string
	logger *zap.Logger
	mu     sync.RWMutex
}

// Config holds database configuration
type Config struct {
	Path string // Database file path
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
		buckets := []string{nodesBucket, poolsBucket, resourcesBucket, volumesBucket, gatewaysBucket}
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
		db:     db,
		path:   cfg.Path,
		logger: logger,
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
	Name      string
	Address   string
	Hostname  string
	State     string
	LastSeen  time.Time
	Version   string
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
	Name      string
	Port      int
	Nodes     string
	Protocol  string
	Replicas  int
	CreatedAt time.Time
	UpdatedAt time.Time
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
	ID        int64
	Name      string
	Resource  string
	Type      GatewayType
	Config    map[string]interface{}
	Status    string
	ActiveNode string
	CreatedAt time.Time
	UpdatedAt time.Time
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

// ==================== VOLUME ====================

// Volume represents a volume in a resource
type Volume struct {
	ID          int64
	ResourceName string
	VolumeName   string
	VolumeID     int
	Pool        string
	SizeGB      int
	Device      string
	CreatedAt   time.Time
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
