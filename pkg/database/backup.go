package database

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Backup state values. A backup is only usable for a restore in StateCompleted;
// everything else exists so a failed or interrupted run is visible instead of
// silently absent.
const (
	// BackupStateRunning is written before the first byte is uploaded. A record
	// stuck here means the controller died mid-backup — which is exactly what an
	// operator needs to see, and is never mistaken for a usable copy.
	BackupStateRunning = "running"
	// BackupStateCompleted means every volume image was uploaded AND verified
	// against the far end's reported size.
	BackupStateCompleted = "completed"
	// BackupStateFailed records a run that did not finish.
	BackupStateFailed = "failed"
)

// Backup kinds. An incremental backup holds only the blocks that changed since
// its Parent, so restoring it needs every backup down the chain to the full one.
const (
	BackupKindFull        = "full"
	BackupKindIncremental = "incremental"
)

// BackupTarget is a repository backups are shipped to.
//
// Secret is stored here in the clear and is deliberately never returned by the
// API. The protection boundary is the database file itself, which bbolt opens
// 0600 — the same boundary the WAN mTLS private key already relies on. Adding
// an encryption layer whose key sits next to the ciphertext on the same host
// would move the secret, not protect it.
type BackupTarget struct {
	Name string
	Kind string // s3 | smb | webdav

	Prefix string

	Bucket   string
	Endpoint string
	Region   string

	Host  string
	Share string

	// User is the S3 access key id, or the SMB/WebDAV username.
	User string
	// Secret is the S3 secret access key, or the SMB/WebDAV password.
	Secret string
	// SecretObscured marks Secret as already in rclone's obscured form.
	SecretObscured bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// BackupVolume records one volume image inside a backup.
type BackupVolume struct {
	VolumeID uint32
	// BackingVolume and Pool identify the LV the snapshot was taken from, so a
	// restore can be reasoned about even when the resource has been recreated.
	BackingVolume string
	Pool          string
	// Object is the target-relative key holding the raw image.
	Object string
	// Bytes is the exact size of the image: the DRBD device's size at backup
	// time, NOT the backing volume's — see BackupManager for why those differ.
	Bytes uint64
	// Ranges is the object listing the "offset length" byte ranges an
	// incremental Object holds, in order. Empty for a full image.
	Ranges string
	// ChangedBytes is how much of the volume an incremental image carries.
	ChangedBytes uint64
	// Snapshot is the thin snapshot this image was read from, kept on Node as
	// the base the next incremental is computed against. Empty once released.
	Snapshot string
	// ReadThroughLUKS marks an image of an encrypted volume that was read
	// through the volume's LUKS container and therefore holds plaintext. An
	// encrypted volume's image without it was copied from the ciphertext by an
	// earlier version and cannot be restored.
	ReadThroughLUKS bool
}

// Backup is one point-in-time copy of a resource on a target.
type Backup struct {
	ID       string
	Resource string
	Target   string
	// Node is the node the snapshot was taken and read on.
	Node string
	// Backend records which implementation wrote the objects (e.g. "rclone").
	Backend string
	State   string
	// Error carries the failure reason for BackupStateFailed.
	Error string
	// Prefix is the target-relative directory holding this backup's objects.
	Prefix string
	// Kind is BackupKindFull or BackupKindIncremental; empty is a full backup
	// taken before incrementals existed.
	Kind string
	// Parent is the backup an incremental was computed against.
	Parent string
	// Schedule names the backup schedule that took this backup; empty for one
	// taken by hand. A schedule's retention only ever deletes its own.
	Schedule   string
	Volumes    []BackupVolume
	TotalBytes uint64
	StartedAt  time.Time
	FinishedAt time.Time
}

// ==================== BACKUP TARGET ====================

// SaveBackupTarget saves or updates a backup target.
func (db *DB) SaveBackupTarget(ctx context.Context, t *BackupTarget) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now

	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("failed to marshal backup target: %w", err)
	}
	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(backupTargetsBucket)).Put([]byte(t.Name), data)
	})
}

// GetBackupTarget retrieves a backup target by name.
func (db *DB) GetBackupTarget(ctx context.Context, name string) (*BackupTarget, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var t BackupTarget
	err := db.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(backupTargetsBucket)).Get([]byte(name))
		if data == nil {
			return fmt.Errorf("backup target %q not found", name)
		}
		return json.Unmarshal(data, &t)
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListBackupTargets lists all backup targets, name-ordered.
func (db *DB) ListBackupTargets(ctx context.Context) ([]*BackupTarget, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	targets := make([]*BackupTarget, 0)
	err := db.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(backupTargetsBucket)).ForEach(func(_, v []byte) error {
			var t BackupTarget
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			targets = append(targets, &t)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	return targets, nil
}

// DeleteBackupTarget deletes a backup target by name.
func (db *DB) DeleteBackupTarget(ctx context.Context, name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(backupTargetsBucket)).Delete([]byte(name))
	})
}

// ==================== BACKUP ====================

// SaveBackup saves or updates a backup record.
func (db *DB) SaveBackup(ctx context.Context, b *Backup) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if b.ID == "" {
		return fmt.Errorf("backup id is required")
	}
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("failed to marshal backup: %w", err)
	}
	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(backupsBucket)).Put([]byte(b.ID), data)
	})
}

// GetBackup retrieves a backup record by id.
func (db *DB) GetBackup(ctx context.Context, id string) (*Backup, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var b Backup
	err := db.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(backupsBucket)).Get([]byte(id))
		if data == nil {
			return fmt.Errorf("backup %q not found", id)
		}
		return json.Unmarshal(data, &b)
	})
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ListBackups lists backup records, newest first. An empty resource or target
// matches everything.
func (db *DB) ListBackups(ctx context.Context, resource, target string) ([]*Backup, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	backups := make([]*Backup, 0)
	err := db.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(backupsBucket)).ForEach(func(_, v []byte) error {
			var b Backup
			if err := json.Unmarshal(v, &b); err != nil {
				return err
			}
			if resource != "" && b.Resource != resource {
				return nil
			}
			if target != "" && b.Target != target {
				return nil
			}
			backups = append(backups, &b)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].StartedAt.After(backups[j].StartedAt) })
	return backups, nil
}

// DeleteBackup deletes a backup record by id.
func (db *DB) DeleteBackup(ctx context.Context, id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(backupsBucket)).Delete([]byte(id))
	})
}
