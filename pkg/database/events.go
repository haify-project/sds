package database

import (
	"context"

	bolt "go.etcd.io/bbolt"
)

// Cluster events (degrade, failover, node loss) used to live only in the
// controller's memory, so a controller restart emptied the history — and under
// Self-HA every failover is a controller restart. The history was gone exactly
// when someone opened it to see what had just happened. Kept here, on the
// replicated metadata volume, it moves with the controller like the audit
// trail does.
//
// Records are the events' JSON as the event package encodes them; this
// package does not interpret them.

const eventBucket = "events"

// AppendEventRecord stores one event, dropping the oldest beyond retention.
func (db *DB) AppendEventRecord(ctx context.Context, record []byte, retention int) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(eventBucket))
		if err != nil {
			return err
		}
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		if err := b.Put(seqKey(seq), record); err != nil {
			return err
		}
		if retention > 0 && b.Stats().KeyN > retention+pruneSlack {
			return pruneAudit(b, retention)
		}
		return nil
	})
}

// RecentEventRecords returns up to limit of the newest stored events, oldest
// first.
func (db *DB) RecentEventRecords(ctx context.Context, limit int) ([][]byte, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var out [][]byte
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(eventBucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Last(); k != nil && (limit <= 0 || len(out) < limit); k, v = c.Prev() {
			out = append(out, append([]byte(nil), v...))
		}
		return nil
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}
