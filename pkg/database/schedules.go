package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

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
	Name     string // unique; equals the target resource name
	Resource string
	Cron     string
	Enabled  bool
	Keep     GFSPolicy
	// LockDays locks every snapshot the schedule takes for that many days:
	// nothing haify does deletes it before then (pkg/controller/snapshot_lock.go).
	LockDays int `json:",omitempty"`
	// FrozenUntil, when in the future, freezes the schedule: it still takes
	// snapshots, but prunes none, and every scheduled snapshot of the
	// resource is locked until then (pkg/controller/snapshot_freeze.go).
	FrozenUntil  time.Time `json:",omitempty"`
	FrozenAt     time.Time `json:",omitempty"`
	FrozenReason string    `json:",omitempty"`
	LastRun      time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
