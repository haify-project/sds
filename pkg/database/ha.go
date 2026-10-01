package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

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
