package database

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Applications (`haify app`): a single-instance PostgreSQL, MySQL, Redis or
// RustFS on a DRBD resource, failed over by drbd-reactor. The promoter config
// and unit on the nodes are what run; this record is how they were made —
// the binaries and ids every node agreed on at creation included — so they
// can be written again to a replica added later, and shown.

const appsBucket = "apps"

// App is one database application.
type App struct {
	Name      string
	Engine    string
	Resource  string
	ServiceIP string
	Port      int
	Vector    bool `json:",omitempty"`
	// Device is the DRBD device the app's filesystem lives on.
	Device string
	// Binaries the nodes agreed on when the app was created.
	Server  string
	Client  string
	Admin   string `json:",omitempty"`
	Init    string `json:",omitempty"`
	Flavor  string `json:",omitempty"`
	Version string `json:",omitempty"`
	// UID and GID of the daemon user, the same on every node.
	UID       int
	GID       int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SaveApp creates or replaces an app record.
func (db *DB) SaveApp(ctx context.Context, app *App) error {
	if app == nil || app.Name == "" {
		return fmt.Errorf("app name is required")
	}
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now()
	if app.CreatedAt.IsZero() {
		app.CreatedAt = now
	}
	app.UpdatedAt = now
	data, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("failed to marshal app: %w", err)
	}
	return db.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(appsBucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(app.Name), data)
	})
}

// GetApp returns the app called name, or nil when there is none.
func (db *DB) GetApp(ctx context.Context, name string) (*App, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var app *App
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(appsBucket))
		if b == nil {
			return nil
		}
		data := b.Get([]byte(name))
		if data == nil {
			return nil
		}
		app = &App{}
		return json.Unmarshal(data, app)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read app %s: %w", name, err)
	}
	return app, nil
}

// ListApps returns every app, by name.
func (db *DB) ListApps(ctx context.Context) ([]*App, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var apps []*App
	err := db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(appsBucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var app App
			if err := json.Unmarshal(v, &app); err != nil {
				return fmt.Errorf("app %s: %w", k, err)
			}
			apps = append(apps, &app)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list apps: %w", err)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}

// GetAppByResource returns the app running on resource, or nil.
func (db *DB) GetAppByResource(ctx context.Context, resource string) (*App, error) {
	apps, err := db.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		if a.Resource == resource {
			return a, nil
		}
	}
	return nil, nil
}

// DeleteApp removes an app record. Removing one that does not exist is not
// an error.
func (db *DB) DeleteApp(ctx context.Context, name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(appsBucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(name))
	})
}
