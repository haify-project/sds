package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
)

var (
	remoteObject = regexp.MustCompile(`sdsbackup:b/([^' ]+)`)
	manifestB64  = regexp.MustCompile(`printf %s '\\''([A-Za-z0-9+/=]+)'\\''`)
)

// bucket wraps an incremental fixture's exec with an in-memory target, so a
// backup's objects and manifest can be listed and read back.
type bucket struct {
	objects   map[string]uint64
	manifests map[string]string
}

func withBucket(f *incrementalFixture) *bucket {
	b := &bucket{objects: map[string]uint64{}, manifests: map[string]string{}}
	dep := f.ctrl.deployment.(*fakeDeploymentClient)
	inner := dep.execFunc
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		switch {
		case strings.Contains(cmd, "lsjson"):
			type entry struct {
				Path string
				Size int64
			}
			var list []entry
			for p, n := range b.objects {
				list = append(list, entry{p, int64(n)})
			}
			out, _ := json.Marshal(list)
			return successExecResult(hosts, string(out)), nil
		case strings.Contains(cmd, " cat 'sdsbackup:b/") && strings.Contains(cmd, "manifest.json"):
			return successExecResult(hosts, b.manifests[remoteObject.FindStringSubmatch(cmd)[1]]), nil
		case strings.Contains(cmd, "rcat"):
			for _, m := range remoteObject.FindAllStringSubmatch(cmd, -1) {
				b.objects[m[1]] = deltaSent
				if strings.HasSuffix(m[1], "manifest.json") {
					raw, err := base64.StdEncoding.DecodeString(manifestB64.FindStringSubmatch(cmd)[1])
					if err != nil {
						panic(err)
					}
					b.manifests[m[1]] = string(raw)
				}
			}
		}
		return inner(ctx, hosts, cmd, opts...)
	}
	return b
}

// The disaster the backups are for: the controller's database is gone. A
// fresh one is pointed at the same target, imports, and restores the newest
// incremental through its whole chain.
func TestImportRebuildsChainsFromManifests(t *testing.T) {
	f := newIncrementalFixture(t)
	b := withBucket(f)
	first := f.backup(t, false)
	second := f.backup(t, false)
	third := f.backup(t, false)
	require.Len(t, b.manifests, 3)

	ctx := context.Background()
	for _, id := range []string{third.ID, second.ID, first.ID} {
		require.NoError(t, f.ctrl.db.DeleteBackup(ctx, id))
	}

	res, err := f.ctrl.backups.ImportBackups(ctx, "offsite", "10.0.0.1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{first.ID, second.ID, third.ID}, res.Imported)
	assert.Empty(t, res.Skipped)

	got, err := f.ctrl.db.GetBackup(ctx, third.ID)
	require.NoError(t, err)
	assert.Equal(t, database.BackupStateCompleted, got.State)
	assert.Equal(t, database.BackupKindIncremental, got.Kind)
	assert.Equal(t, second.ID, got.Parent)
	assert.Equal(t, third.Volumes[0].Ranges, got.Volumes[0].Ranges)
	assert.Equal(t, third.Volumes[0].ChangedBytes, got.Volumes[0].ChangedBytes)
	assert.Equal(t, third.Prefix, got.Prefix)
	// The fixture's nodes are not registered, so this is "another cluster":
	// no snapshot name is adopted, and the next backup is a full one.
	assert.Empty(t, got.Volumes[0].Snapshot)

	f.cmds = nil
	_, err = f.ctrl.backups.RestoreBackup(ctx, third.ID, "", "")
	require.NoError(t, err)
	assert.Len(t, f.ran("seek_bytes"), 2, "both incrementals are replayed on the imported full one")

	again, err := f.ctrl.backups.ImportBackups(ctx, "offsite", "10.0.0.1")
	require.NoError(t, err)
	assert.Empty(t, again.Imported, "importing twice records nothing twice")
	assert.Len(t, again.Skipped, 3)
}

// On the same cluster, the newest backup's base snapshot is still on its node
// and is adopted, so the chain carries on incrementally.
func TestImportAdoptsTheNewestBaseOnItsOwnCluster(t *testing.T) {
	f := newIncrementalFixture(t)
	withBucket(f)
	ctx := context.Background()
	first := f.backup(t, false)
	second := f.backup(t, false)
	require.NoError(t, f.ctrl.db.SaveNode(ctx, &database.Node{Name: "n1", Address: second.Node}))
	for _, id := range []string{second.ID, first.ID} {
		require.NoError(t, f.ctrl.db.DeleteBackup(ctx, id))
	}

	_, err := f.ctrl.backups.ImportBackups(ctx, "offsite", "10.0.0.1")
	require.NoError(t, err)
	got, err := f.ctrl.db.GetBackup(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, second.Volumes[0].Snapshot, got.Volumes[0].Snapshot)
	old, err := f.ctrl.db.GetBackup(ctx, first.ID)
	require.NoError(t, err)
	assert.Empty(t, old.Volumes[0].Snapshot)

	next := f.backup(t, false)
	assert.Equal(t, database.BackupKindIncremental, next.Kind)
	assert.Equal(t, second.ID, next.Parent)
}

func TestImportSkipsABackupMissingAnImage(t *testing.T) {
	manifest := `{"version":2,"id":"data_1","resource":"data","node":"n1","kind":"full",
	  "created_at":"2026-10-01T02:30:00Z","volumes":[{"volume_id":0,"object":"data/data_1/volume-0.img.gz","bytes":10}]}`
	_, err := backupFromManifest(manifest, "data/data_1", "offsite", map[string]uint64{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing from the target")

	rec, err := backupFromManifest(manifest, "data/data_1", "offsite", map[string]uint64{"data/data_1/volume-0.img.gz": 7})
	require.NoError(t, err)
	assert.Equal(t, "offsite", rec.Target)
	assert.Equal(t, database.BackupStateCompleted, rec.State)
}
