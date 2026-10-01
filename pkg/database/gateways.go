package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

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
