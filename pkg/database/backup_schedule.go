package database

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// BackupSchedule ships a resource to a target on a cron, and prunes the
// backups it made there per Keep. One schedule per resource and target.
type BackupSchedule struct {
	// Name is BackupScheduleName(Resource, Target).
	Name     string
	Resource string
	Target   string
	Cron     string
	Enabled  bool
	Keep     GFSPolicy
	LastRun  time.Time
	// LastBackup is the id the last run produced; LastError why it produced
	// none. Exactly one of them is set once the schedule has run.
	LastBackup string
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// BackupScheduleName is the key of the schedule backing resource up to target.
func BackupScheduleName(resource, target string) string {
	return resource + "@" + target
}

// SaveBackupSchedule saves or updates a backup schedule.
func (db *DB) SaveBackupSchedule(ctx context.Context, s *BackupSchedule) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("failed to marshal backup schedule: %w", err)
	}
	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(backupSchedulesBucket)).Put([]byte(s.Name), data)
	})
}

// GetBackupSchedule retrieves a backup schedule by name.
func (db *DB) GetBackupSchedule(ctx context.Context, name string) (*BackupSchedule, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var s BackupSchedule
	err := db.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(backupSchedulesBucket)).Get([]byte(name))
		if data == nil {
			return fmt.Errorf("backup schedule %q not found", name)
		}
		return json.Unmarshal(data, &s)
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListBackupSchedules lists all backup schedules, name-ordered.
func (db *DB) ListBackupSchedules(ctx context.Context) ([]*BackupSchedule, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	schedules := make([]*BackupSchedule, 0)
	err := db.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(backupSchedulesBucket)).ForEach(func(_, v []byte) error {
			var s BackupSchedule
			if err := json.Unmarshal(v, &s); err != nil {
				return err
			}
			schedules = append(schedules, &s)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(schedules, func(i, j int) bool { return schedules[i].Name < schedules[j].Name })
	return schedules, nil
}

// DeleteBackupSchedule deletes a backup schedule by name.
func (db *DB) DeleteBackupSchedule(ctx context.Context, name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(backupSchedulesBucket))
		if b.Get([]byte(name)) == nil {
			return fmt.Errorf("backup schedule %q not found", name)
		}
		return b.Delete([]byte(name))
	})
}
