package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

// withMigrations swaps the chain for the duration of a test. The runner is what
// is under test here; the real chain is exercised separately by
// TestRealMigrationChainIsWellFormed.
func withMigrations(t *testing.T, chain []migration) {
	t.Helper()
	original := migrations
	migrations = chain
	t.Cleanup(func() { migrations = original })
}

// writeLegacyDatabase produces a database in the shape a controller from before
// schema versioning existed would have left: real buckets, real records, no
// _meta bucket at all.
func writeLegacyDatabase(t *testing.T, path string) {
	t.Helper()
	bdb, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.NoError(t, bdb.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(resourcesBucket))
		if err != nil {
			return err
		}
		return b.Put([]byte("legacy-res"), []byte(`{"Name":"legacy-res","Port":7000}`))
	}))
	require.NoError(t, bdb.Close())
}

// stampVersion writes a raw schema version value directly, standing in for a
// database left behind by some other binary.
func stampVersion(t *testing.T, path string, raw string) {
	t.Helper()
	bdb, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.NoError(t, bdb.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(metaBucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(schemaVersionKey), []byte(raw))
	}))
	require.NoError(t, bdb.Close())
}

func TestFreshDatabaseIsStampedWithCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sds.db")

	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	// Teardown for a database under t.TempDir(). The tests below that care about
	// the close itself — the ones checking a migration's backup is a restorable
	// file — assert on it with require.NoError instead. Where it is only
	// teardown the error is dropped: bolt fsyncs at commit, so a failed unmap
	// cannot undo anything the test has already read back.
	defer func() { _ = db.Close() }()

	version, err := db.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, SchemaVersion(), version)
}

func TestLegacyDatabaseIsTreatedAsVersionOneNotAsFresh(t *testing.T) {
	// A migration to version 2 exists, so "fresh" and "legacy" produce visibly
	// different outcomes: a fresh database is stamped 2 and skips the step, a
	// legacy one must actually run it.
	applied := false
	withMigrations(t, []migration{{
		to:   2,
		name: "test-step",
		apply: func(tx *bolt.Tx) error {
			applied = true
			// The step must observe the pre-existing record, proving it runs
			// against the real legacy content and not an empty database.
			b := tx.Bucket([]byte(resourcesBucket))
			if b == nil || b.Get([]byte("legacy-res")) == nil {
				return errors.New("legacy record not visible to migration")
			}
			return nil
		},
	}})

	path := filepath.Join(t.TempDir(), "sds.db")
	writeLegacyDatabase(t, path)

	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	assert.True(t, applied, "migration must run against an unversioned database")

	version, err := db.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, 2, version)

	// The pre-existing data survived the upgrade.
	res, err := db.GetResource(t.Context(), "legacy-res")
	require.NoError(t, err)
	assert.Equal(t, "legacy-res", res.Name)
}

func TestFreshDatabaseSkipsMigrations(t *testing.T) {
	withMigrations(t, []migration{{
		to:   2,
		name: "must-not-run",
		apply: func(tx *bolt.Tx) error {
			return errors.New("migration ran against a brand new database")
		},
	}})

	path := filepath.Join(t.TempDir(), "sds.db")
	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	version, err := db.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, 2, version)
}

func TestNewerSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sds.db")
	writeLegacyDatabase(t, path)
	stampVersion(t, path, "99")

	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.Error(t, err)
	assert.Nil(t, db)
	assert.True(t, IsSchemaIncompatible(err), "refusal must be recognisable to the start-up path")

	var schemaErr *IncompatibleSchemaError
	require.True(t, errors.As(err, &schemaErr))
	assert.Equal(t, 99, schemaErr.Found)
	assert.Equal(t, SchemaVersion(), schemaErr.Supported)

	msg := err.Error()
	// The message has to be enough to act on without reading the source: which
	// database, which versions, and what to do next.
	assert.Contains(t, msg, path)
	assert.Contains(t, msg, "99")
	assert.Contains(t, msg, strconv.Itoa(SchemaVersion()))
	assert.Contains(t, msg, ".bak")

	// Refusing means refusing to write, too: the version on disk is untouched.
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, after)
	stamped := readRawVersion(t, path)
	assert.Equal(t, "99", stamped)
}

func TestUnreadableSchemaVersionIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sds.db")
	writeLegacyDatabase(t, path)
	stampVersion(t, path, "not-a-number")

	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.Error(t, err)
	assert.Nil(t, db)
	assert.True(t, IsSchemaIncompatible(err))
	assert.Contains(t, err.Error(), "not-a-number")
}

func TestMigrationsRunInOrder(t *testing.T) {
	var order []int
	step := func(to int) migration {
		return migration{
			to:   to,
			name: fmt.Sprintf("step-%d", to),
			apply: func(tx *bolt.Tx) error {
				order = append(order, to)
				b, err := tx.CreateBucketIfNotExists([]byte("migration_marks"))
				if err != nil {
					return err
				}
				return b.Put([]byte(strconv.Itoa(to)), []byte("done"))
			},
		}
	}
	withMigrations(t, []migration{step(2), step(3), step(4)})

	path := filepath.Join(t.TempDir(), "sds.db")
	writeLegacyDatabase(t, path)

	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	assert.Equal(t, []int{2, 3, 4}, order)

	version, err := db.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, 4, version)
}

func TestAlreadyCurrentDatabaseRunsNothing(t *testing.T) {
	ran := 0
	withMigrations(t, []migration{{
		to:    2,
		name:  "step-2",
		apply: func(tx *bolt.Tx) error { ran++; return nil },
	}})

	path := filepath.Join(t.TempDir(), "sds.db")
	writeLegacyDatabase(t, path)

	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.Equal(t, 1, ran)

	// Reopening an up-to-date database must not re-run the chain, and must not
	// leave a second backup copy behind either.
	db, err = Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	assert.Equal(t, 1, ran)
}

func TestFailedMigrationLeavesNoPartialState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sds.db")
	writeLegacyDatabase(t, path)

	mark := func(tx *bolt.Tx, key string) error {
		b, err := tx.CreateBucketIfNotExists([]byte("migration_marks"))
		if err != nil {
			return err
		}
		return b.Put([]byte(key), []byte("done"))
	}

	withMigrations(t, []migration{
		{to: 2, name: "step-2", apply: func(tx *bolt.Tx) error { return mark(tx, "two") }},
		{to: 3, name: "step-3-fails", apply: func(tx *bolt.Tx) error {
			// Write first, then fail: the write must roll back with the step.
			if err := mark(tx, "three"); err != nil {
				return err
			}
			return errors.New("boom")
		}},
	})

	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.Error(t, err)
	assert.Nil(t, db)
	assert.Contains(t, err.Error(), "step-3-fails")
	// The message must say where the database now stands and where the copy is.
	assert.Contains(t, err.Error(), "version 2")
	assert.Contains(t, err.Error(), ".v1.bak")

	// Reopen with only the step that succeeded: the database must be exactly at
	// version 2, with step 2's write present and step 3's write absent.
	withMigrations(t, []migration{
		{to: 2, name: "step-2", apply: func(tx *bolt.Tx) error { return mark(tx, "two") }},
	})

	db, err = Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	version, err := db.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, 2, version)

	require.NoError(t, db.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("migration_marks"))
		require.NotNil(t, b)
		assert.NotNil(t, b.Get([]byte("two")), "committed step must survive")
		assert.Nil(t, b.Get([]byte("three")), "rolled-back step must leave nothing")
		return nil
	}))

	// And the legacy record is still there — a failed upgrade must not eat data.
	res, err := db.GetResource(t.Context(), "legacy-res")
	require.NoError(t, err)
	assert.Equal(t, "legacy-res", res.Name)
}

func TestPreMigrationBackupIsWrittenAndUsable(t *testing.T) {
	withMigrations(t, []migration{{
		to:   2,
		name: "delete-everything",
		apply: func(tx *bolt.Tx) error {
			return tx.DeleteBucket([]byte(resourcesBucket))
		},
	}})

	dir := t.TempDir()
	path := filepath.Join(dir, "sds.db")
	writeLegacyDatabase(t, path)

	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	backup := path + ".v1.bak"
	require.FileExists(t, backup)
	assert.NoFileExists(t, backup+".partial")

	// The copy has to be a database an operator can actually restore from, with
	// the pre-migration content intact.
	restored, err := bolt.Open(backup, 0600, &bolt.Options{Timeout: 5 * time.Second})
	require.NoError(t, err)
	defer func() { _ = restored.Close() }()
	require.NoError(t, restored.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(resourcesBucket))
		require.NotNil(t, b, "backup must predate the destructive migration")
		assert.NotNil(t, b.Get([]byte("legacy-res")))
		return nil
	}))
}

func TestNoBackupWithoutAMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sds.db")

	db, err := Open(&Config{Path: path}, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".bak", "a start with nothing to migrate must not copy the database")
	}
}

func TestRealMigrationChainIsWellFormed(t *testing.T) {
	require.NoError(t, validateMigrationChain(migrations))
	assert.GreaterOrEqual(t, SchemaVersion(), legacySchemaVersion)
}

func TestMalformedMigrationChainIsRejected(t *testing.T) {
	noop := func(tx *bolt.Tx) error { return nil }

	cases := map[string][]migration{
		"gap":         {{to: 2, name: "a", apply: noop}, {to: 4, name: "b", apply: noop}},
		"repeat":      {{to: 2, name: "a", apply: noop}, {to: 2, name: "b", apply: noop}},
		"below base":  {{to: 1, name: "a", apply: noop}},
		"no name":     {{to: 2, apply: noop}},
		"no function": {{to: 2, name: "a"}},
	}
	for name, chain := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateMigrationChain(chain))
		})
	}

	// A malformed chain must stop Open rather than migrate half a database.
	withMigrations(t, cases["gap"])
	db, err := Open(&Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.Error(t, err)
	assert.Nil(t, db)
}

// readRawVersion reads the stored version without going through Open, so a
// refused database can be inspected.
func readRawVersion(t *testing.T, path string) string {
	t.Helper()
	bdb, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	require.NoError(t, err)
	defer func() { _ = bdb.Close() }()

	var raw string
	require.NoError(t, bdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(metaBucket))
		require.NotNil(t, b)
		raw = string(b.Get([]byte(schemaVersionKey)))
		return nil
	}))
	return raw
}
