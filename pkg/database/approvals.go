package database

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Two-person approval requests ([rbac.approval], pkg/controller/approval.go).
// They live in the database so a request made before a failover can be
// approved, and the approved call made, on the controller that took over.

const approvalsBucket = "approvals"

// Approval states. A pending or approved request past ExpiresAt is expired;
// that is computed when it is read, never stored.
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
	ApprovalUsed     = "used"
	ApprovalExpired  = "expired"
)

// Approval is one call waiting for, or holding, a second person's approval.
type Approval struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	// Digest identifies the exact call: method and arguments. Only that call
	// is approved.
	Digest string `json:"digest"`
	// Request is the call's arguments as JSON, secrets masked, for the
	// approver to read.
	Request   string    `json:"request"`
	Requester string    `json:"requester"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	State     string    `json:"state"`
	DecidedBy string    `json:"decided_by,omitempty"`
	DecidedAt time.Time `json:"decided_at,omitempty"`
}

// EffectiveState is State, or expired once a still-open request has run out.
func (a *Approval) EffectiveState(now time.Time) string {
	if (a.State == ApprovalPending || a.State == ApprovalApproved) && now.After(a.ExpiresAt) {
		return ApprovalExpired
	}
	return a.State
}

// SaveApproval creates or replaces a request.
func (db *DB) SaveApproval(ctx context.Context, a *Approval) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(approvalsBucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(a.ID), data)
	})
}

// GetApproval returns one request.
func (db *DB) GetApproval(ctx context.Context, id string) (*Approval, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var a *Approval
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(approvalsBucket))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(id))
		if v == nil {
			return nil
		}
		a = &Approval{}
		return json.Unmarshal(v, a)
	})
	if err == nil && a == nil {
		err = fmt.Errorf("no approval request %q", id)
	}
	return a, err
}

// ListApprovals returns every request, newest first.
func (db *DB) ListApprovals(ctx context.Context) ([]*Approval, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var out []*Approval
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(approvalsBucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, v []byte) error {
			var a Approval
			if json.Unmarshal(v, &a) == nil {
				out = append(out, &a)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, err
}

// PruneApprovals drops requests that closed (or expired) before cutoff. The
// audit trail, not this list, is the record of what was approved.
func (db *DB) PruneApprovals(ctx context.Context, cutoff time.Time) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(approvalsBucket))
		if b == nil {
			return nil
		}
		var doomed [][]byte
		err := b.ForEach(func(k, v []byte) error {
			var a Approval
			if json.Unmarshal(v, &a) == nil && a.ExpiresAt.Before(cutoff) {
				doomed = append(doomed, append([]byte(nil), k...))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range doomed {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}
