package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ==================== HA CONFIG ====================

// HaConfig represents a highly available configuration: what `ha create` was
// asked for. The promoter config on the nodes is what runs; this is the record
// of how it was made, kept whole so it can be shown and made again.
type HaConfig struct {
	Resource   string
	VIP        string
	MountPoint string
	FsType     string
	Services   []string
	// OcfAgents are the OCF agents appended after the services, in order.
	OcfAgents []HaOcfAgent `json:",omitempty"`
	// StartItems, when set, is the promoter's start list exactly as given;
	// it then replaces Services, MountPoint, VIP and OcfAgents as the order.
	StartItems []HaStartItem `json:",omitempty"`
	// PreferredNodes orders where drbd-reactor starts the resource
	// (preferred-nodes), and PreferredNodesPolicy says whether that also
	// moves it back ("always") or only picks where it starts ("start-only").
	PreferredNodes       []string `json:",omitempty"`
	PreferredNodesPolicy string   `json:",omitempty"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// HaOcfAgent is one OCF resource agent in an HA promoter's start list.
type HaOcfAgent struct {
	Provider string
	Name     string
	Instance string
	Params   map[string]string `json:",omitempty"`
}

// HaStartItem is one entry of an explicit start list: a systemd unit or an
// OCF agent.
type HaStartItem struct {
	SystemdUnit string      `json:",omitempty"`
	Ocf         *HaOcfAgent `json:",omitempty"`
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
