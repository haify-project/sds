package database

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Shipping the audit trail off the cluster.
//
// The trail in this database is only as trustworthy as the controller host:
// whoever holds it can rewrite the file. So each entry is also sent to a
// destination outside the cluster (pkg/controller/audit_ship.go). The trail
// itself is the outbox: every destination has a cursor here, the sequence
// number of the last entry it acknowledged, and the shipper sends what comes
// after it. The cursor sits on the replicated metadata volume with the trail,
// so a controller that takes over after a failover resumes where the last one
// stopped, and entries written while a destination was down are sent once it
// is back — nothing between two acknowledged batches is skipped.

const auditShipBucket = "audit_ship"

// AuditRecord is an audit entry with its position in the trail.
type AuditRecord struct {
	Seq   uint64      `json:"seq"`
	Event *AuditEvent `json:"event"`
}

// auditCutoff is the time before which entries are past their retention, or
// zero when entries are kept until the cap.
func (db *DB) auditCutoff() time.Time {
	if db.auditMaxAge <= 0 {
		return time.Time{}
	}
	return time.Now().Add(-db.auditMaxAge)
}

// AuditEventsAfter returns up to limit entries that follow seq, oldest first.
func (db *DB) AuditEventsAfter(ctx context.Context, seq uint64, limit int) ([]AuditRecord, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var out []AuditRecord
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(auditBucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Seek(seqKey(seq + 1)); k != nil && len(out) < limit; k, v = c.Next() {
			var ev AuditEvent
			if err := json.Unmarshal(v, &ev); err != nil {
				// Shipped as a gap would be silent; an unreadable entry still
				// has a sequence number the receiver can see is missing.
				continue
			}
			out = append(out, AuditRecord{Seq: binary.BigEndian.Uint64(k), Event: &ev})
		}
		return nil
	})
	return out, err
}

// AuditShipCursor is the last entry destination acknowledged; zero before the
// first.
func (db *DB) AuditShipCursor(ctx context.Context, destination string) (uint64, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var seq uint64
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(auditShipBucket))
		if b == nil {
			return nil
		}
		if v := b.Get([]byte(destination)); len(v) == 8 {
			seq = binary.BigEndian.Uint64(v)
		}
		return nil
	})
	return seq, err
}

// SetAuditShipCursor records that destination acknowledged everything up to
// and including seq.
func (db *DB) SetAuditShipCursor(ctx context.Context, destination string, seq uint64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(auditShipBucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(destination), seqKey(seq))
	})
}

// PruneExpiredAudit drops entries past their retention, oldest first, at most
// batch of them per call, and returns how many it dropped.
func (db *DB) PruneExpiredAudit(ctx context.Context, batch int) (int, error) {
	cutoff := db.auditCutoff()
	if cutoff.IsZero() {
		return 0, nil
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	dropped := 0
	err := db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(auditBucket))
		if b == nil {
			return nil
		}
		var doomed [][]byte
		c := b.Cursor()
		for k, v := c.First(); k != nil && len(doomed) < batch; k, v = c.Next() {
			var ev AuditEvent
			if json.Unmarshal(v, &ev) == nil && !ev.Timestamp.Before(cutoff) {
				break
			}
			doomed = append(doomed, append([]byte(nil), k...))
		}
		// Keys are deleted after the walk; see pruneAudit.
		for _, k := range doomed {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		dropped = len(doomed)
		return nil
	})
	return dropped, err
}

// TakeAuditTruncated returns how many entries the cap dropped before their
// retention ran out since the last call, and resets the count.
func (db *DB) TakeAuditTruncated() uint64 {
	return db.auditTruncated.Swap(0)
}
