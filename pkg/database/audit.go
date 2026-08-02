package database

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The audit trail lives in the database rather than only in the process log
// because the controller relocates: with Self-HA it runs on whichever node
// holds the metadata volume, so a journal-only trail is split across the nodes
// that happened to be active and is unreadable exactly when it matters. The
// database file sits on that same replicated volume, so the history moves with
// the controller.

const auditBucket = "audit"

// DefaultAuditRetention caps the trail. It is stored on a small replicated
// volume shared with the rest of the controller's state, so it cannot be
// allowed to grow without bound; at a few hundred bytes an entry this is a
// handful of megabytes, and covers far more history than anyone reads.
const DefaultAuditRetention = 20000

// pruneSlack lets entries accumulate past the cap before a prune runs, so the
// common case is a plain append rather than an append plus a delete scan.
const pruneSlack = 500

// AuditEvent is one recorded API call.
type AuditEvent struct {
	Timestamp time.Time     `json:"timestamp"`
	Method    string        `json:"method"`
	Client    string        `json:"client"`
	User      string        `json:"user,omitempty"`
	Target    string        `json:"target,omitempty"`
	Result    string        `json:"result"`
	Granted   bool          `json:"granted"`
	Latency   time.Duration `json:"latency"`
	Error     string        `json:"error,omitempty"`
	Node      string        `json:"node,omitempty"`
}

// AuditFilter narrows a listing. Zero values mean "no constraint".
type AuditFilter struct {
	Limit        int
	Method       string
	Target       string
	User         string
	FailuresOnly bool
	Since        time.Time
}

// DefaultAuditLimit is returned when a caller asks for no particular number.
const DefaultAuditLimit = 200

// seqKey renders a bolt sequence number as a big-endian key so that bolt's
// byte-order iteration is also chronological order.
func seqKey(seq uint64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, seq)
	return k
}

// auditRetention returns the configured cap, falling back to the default.
func (db *DB) auditRetention() int {
	if db.auditCap > 0 {
		return db.auditCap
	}
	return DefaultAuditRetention
}

// AppendAuditEvent records one API call. It is called on the request path, so
// it does the minimum: one sequential put, and a prune only once the trail has
// drifted meaningfully past the cap.
//
// The write is synchronous, which costs a commit (single-digit milliseconds)
// per audited call. That is deliberate: audited calls are state changes, which
// already take orders of magnitude longer, and a trail that can lose its most
// recent entries in a crash is worth much less than one that cannot.
func (db *DB) AppendAuditEvent(ctx context.Context, ev *AuditEvent) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("failed to marshal audit event: %w", err)
	}

	retention := db.auditRetention()
	return db.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(auditBucket))
		if err != nil {
			return err
		}
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		if err := b.Put(seqKey(seq), data); err != nil {
			return err
		}
		if b.Stats().KeyN > retention+pruneSlack {
			return pruneAudit(b, retention)
		}
		return nil
	})
}

// pruneAudit drops the oldest entries until the bucket is back at the cap.
// Keys are sequence numbers, so the cursor walks oldest-first.
//
// The keys are collected before anything is deleted. Calling Cursor.Delete()
// and then Next() advances past the key that followed the deleted one, so
// deleting during the walk removes a scattered subset — it leaves the trail
// full of holes and discards newer entries while keeping older ones.
func pruneAudit(b *bolt.Bucket, retention int) error {
	excess := b.Stats().KeyN - retention
	if excess <= 0 {
		return nil
	}

	doomed := make([][]byte, 0, excess)
	c := b.Cursor()
	for k, _ := c.First(); k != nil && len(doomed) < excess; k, _ = c.Next() {
		// The key is only valid for the life of the transaction, and it is
		// used within it, but bolt reuses the backing page buffer across
		// cursor moves — so it has to be copied.
		doomed = append(doomed, append([]byte(nil), k...))
	}
	for _, k := range doomed {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// matches reports whether an event satisfies the filter.
func (f *AuditFilter) matches(ev *AuditEvent) bool {
	if f.Method != "" && !strings.EqualFold(ev.Method, f.Method) {
		return false
	}
	if f.Target != "" && !strings.EqualFold(ev.Target, f.Target) {
		return false
	}
	if f.User != "" && !strings.EqualFold(ev.User, f.User) {
		return false
	}
	if f.FailuresOnly && ev.Result == "OK" {
		return false
	}
	if !f.Since.IsZero() && ev.Timestamp.Before(f.Since) {
		return false
	}
	return true
}

// ListAuditEvents returns matching entries newest first, and the total number
// of entries held. It walks backwards from the newest key and stops as soon as
// the limit is reached, so the cost is proportional to what is returned rather
// than to the size of the trail — except when a filter matches nothing, which
// is bounded by the retention cap.
func (db *DB) ListAuditEvents(ctx context.Context, filter AuditFilter) ([]*AuditEvent, int, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultAuditLimit
	}

	var events []*AuditEvent
	var total int
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(auditBucket))
		if b == nil {
			// No call has been audited yet; an empty trail is not an error.
			return nil
		}
		total = b.Stats().KeyN

		c := b.Cursor()
		for k, v := c.Last(); k != nil && len(events) < limit; k, v = c.Prev() {
			var ev AuditEvent
			if err := json.Unmarshal(v, &ev); err != nil {
				// One unreadable record must not hide the rest of the trail.
				continue
			}
			if filter.matches(&ev) {
				events = append(events, &ev)
			}
		}
		return nil
	})

	return events, total, err
}
