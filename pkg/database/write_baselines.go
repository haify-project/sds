package database

import (
	"context"

	bolt "go.etcd.io/bbolt"
)

// Learned write-rate baselines (pkg/controller/write_anomaly.go), one opaque
// JSON document per resource. They take weeks to learn by hour of the week,
// so they live here rather than in memory, and move with the controller on
// failover.

const writeBaselinesBucket = "write_baselines"

// SaveWriteBaseline stores resource's baseline.
func (db *DB) SaveWriteBaseline(ctx context.Context, resource string, data []byte) error {
	buf := append([]byte(nil), data...)
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(writeBaselinesBucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(resource), buf)
	})
}

// LoadWriteBaseline returns resource's baseline, or nil when none is stored.
func (db *DB) LoadWriteBaseline(ctx context.Context, resource string) ([]byte, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var out []byte
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(writeBaselinesBucket))
		if b == nil {
			return nil
		}
		if v := b.Get([]byte(resource)); v != nil {
			out = append([]byte(nil), v...)
		}
		return nil
	})
	return out, err
}
