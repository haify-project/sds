package database

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// NotifyChannel is one place alerts are delivered to.
//
// These live in the database rather than in controller.toml so a channel can be
// added, silenced or removed from the UI without an operator editing a file and
// restarting the controller — which, for an alerting path, means a window with
// no alerting at all, entered deliberately, on a running cluster.
//
// The toml [alert] receivers still work and are read at startup as before.
// Between the two, a channel defined here wins nothing and loses nothing: both
// sets are subscribed. The file is for configuration management, this is for
// the people on call.
//
// Secret is stored in the clear and is deliberately never returned by the API,
// exactly as BackupTarget.Secret is, and for the same reason: the protection
// boundary is the 0600 database file, and an encryption key kept beside its
// ciphertext protects nothing.
type NotifyChannel struct {
	// Name identifies the channel and is the primary key.
	Name string
	// Kind is the message format: generic, feishu, slack, wecom, dingtalk.
	Kind string
	// URL is the bot or receiver endpoint.
	URL string
	// MinSeverity drops anything less severe: info (default), warning, critical.
	MinSeverity string
	// Types, when non-empty, restricts this channel to those event types. Empty
	// means every type.
	Types []string
	// Headers are sent with every request — an auth token, a routing key.
	Headers map[string]string
	// Secret is the signing secret for kinds that authenticate that way
	// (DingTalk's 加签). Never returned over the API.
	Secret string
	// Enabled is the mute switch. A muted channel keeps its configuration, so
	// silencing a noisy pager during an incident does not mean re-entering a
	// bot URL afterwards, at the worst possible moment to be doing that.
	Enabled bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// SaveNotifyChannel saves or updates a channel.
func (db *DB) SaveNotifyChannel(ctx context.Context, c *NotifyChannel) error {
	if c == nil || c.Name == "" {
		return fmt.Errorf("notification channel name is required")
	}
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now

	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal notification channel: %w", err)
	}
	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(notifyChannelsBucket)).Put([]byte(c.Name), data)
	})
}

// GetNotifyChannel retrieves one channel by name.
func (db *DB) GetNotifyChannel(ctx context.Context, name string) (*NotifyChannel, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var c NotifyChannel
	err := db.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(notifyChannelsBucket)).Get([]byte(name))
		if data == nil {
			return fmt.Errorf("notification channel %q not found", name)
		}
		return json.Unmarshal(data, &c)
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListNotifyChannels lists every channel, name-ordered.
func (db *DB) ListNotifyChannels(ctx context.Context) ([]*NotifyChannel, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	out := make([]*NotifyChannel, 0)
	err := db.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(notifyChannelsBucket)).ForEach(func(_, v []byte) error {
			var c NotifyChannel
			if err := json.Unmarshal(v, &c); err != nil {
				return err
			}
			out = append(out, &c)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DeleteNotifyChannel removes a channel by name.
func (db *DB) DeleteNotifyChannel(ctx context.Context, name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(notifyChannelsBucket)).Delete([]byte(name))
	})
}
