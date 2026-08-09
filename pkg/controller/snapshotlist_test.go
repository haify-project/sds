package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
)

// Listing LVM snapshots. The bug this covers: the lvs command carried a literal
// "VG/LV" placeholder copied from the man page, which made lvs exit 5 while
// still printing the snapshots it found. Callers that checked the exit status —
// this one — discarded the output and reported an empty list on a pool holding
// 27 snapshots. The scheduler survived it only because it reads output
// regardless of exit status, so retention kept working and hid the defect.

// realLvsOutput is what a node emits now that the placeholder is gone:
// "name|size_bytes|time|origin".
const realLvsOutput = `  openclaw_data_sched_20260808T110000Z|6442450944|2026-08-08 11:00:03 +0000|openclaw_data
  openclaw_data_sched_20260809T130000Z|6442450944|2026-08-09 13:00:02 +0000|openclaw_data
  other_data_sched_20260809T130000Z|1073741824|2026-08-09 13:00:04 +0000|other_data`

func snapshotDeployment(out string) *fakeDeploymentClient {
	return &fakeDeploymentClient{
		lvListSnapshotsFunc: func(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error) {
			return successExecResult(hosts, out), nil
		},
	}
}

func TestListLvmSnapshotsParsesEveryColumn(t *testing.T) {
	ctrl := newBasicTestController(snapshotDeployment(realLvsOutput))

	snaps, err := ctrl.storage.ListLvmSnapshots(context.Background(), "sds_sdspool", "node-e", "")
	require.NoError(t, err)
	require.Len(t, snaps, 3)

	first := snaps[0]
	assert.Equal(t, "openclaw_data_sched_20260808T110000Z", first.Name)
	assert.Equal(t, uint64(6), first.SizeGB, "size was previously hardcoded to 0")
	assert.Equal(t, "2026-08-08 11:00:03 +0000", first.CreatedAt, "lv_time was fetched and thrown away")
	assert.Equal(t, "openclaw_data", first.Origin)
}

func TestListLvmSnapshotsSurfacesAFailedCommand(t *testing.T) {
	// The defect's real damage was silence: a broken command read as an empty
	// pool. A failure has to be an error, not an empty slice.
	dep := &fakeDeploymentClient{
		lvListSnapshotsFunc: func(ctx context.Context, hosts []string, vgName string) (*deployment.ExecResult, error) {
			return failedExecResult(hosts, `  Volume group "VG" not found.`), nil
		},
	}
	ctrl := newBasicTestController(dep)

	_, err := ctrl.storage.ListLvmSnapshots(context.Background(), "sds_sdspool", "node-e", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sds_sdspool")
}

func TestListLvmSnapshotsToleratesShortAndBlankLines(t *testing.T) {
	out := "\n  openclaw_data_sched_20260809T130000Z|6442450944|2026-08-09 13:00:02 +0000|openclaw_data\n  truncated|123\n"
	ctrl := newBasicTestController(snapshotDeployment(out))

	snaps, err := ctrl.storage.ListLvmSnapshots(context.Background(), "sds_sdspool", "node-e", "")
	require.NoError(t, err)
	require.Len(t, snaps, 1)
}

func TestParseSnapshotLine(t *testing.T) {
	snap, ok := parseSnapshotLine("  a_sched_20260809T130000Z|6442450944|2026-08-09 13:00:02 +0000|a", "vg")
	require.True(t, ok)
	assert.Equal(t, "a_sched_20260809T130000Z", snap.Name)
	assert.Equal(t, "vg", snap.Volume)
	assert.Equal(t, "a", snap.Origin)

	// The time column contains spaces, which is why the separator is a pipe and
	// not whitespace; splitting on spaces would have read it as three columns.
	assert.Equal(t, "2026-08-09 13:00:02 +0000", snap.CreatedAt)

	_, ok = parseSnapshotLine("", "vg")
	assert.False(t, ok)
	_, ok = parseSnapshotLine("a|b|c", "vg")
	assert.False(t, ok)
	_, ok = parseSnapshotLine("|1|t|o", "vg")
	assert.False(t, ok, "a nameless row is not a snapshot")
}

func TestListLvmSnapshotsFiltersByResource(t *testing.T) {
	// The pool holds every resource's snapshots on that node. `--resource` used
	// to be accepted, printed in the heading, and then ignored, so listing two
	// different resources returned identical lists of everything.
	ctrl := newBasicTestController(snapshotDeployment(realLvsOutput))
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	ctx := context.Background()
	require.NoError(t, db.SaveVolume(ctx, &database.Volume{
		ResourceName: "openclaw", VolumeName: "openclaw_data", VolumeID: 0, Pool: "sds_sdspool",
	}))
	require.NoError(t, db.SaveVolume(ctx, &database.Volume{
		ResourceName: "other", VolumeName: "other_data", VolumeID: 0, Pool: "sds_sdspool",
	}))

	snaps, err := ctrl.storage.ListLvmSnapshots(ctx, "sds_sdspool", "node-e", "openclaw")
	require.NoError(t, err)
	require.Len(t, snaps, 2)
	for _, s := range snaps {
		assert.Equal(t, "openclaw_data", s.Origin)
	}

	// A different resource must yield a different list, which is the whole
	// point of the flag.
	snaps, err = ctrl.storage.ListLvmSnapshots(ctx, "sds_sdspool", "node-e", "other")
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Equal(t, "other_data", snaps[0].Origin)

	// And an empty resource still lists the whole group.
	snaps, err = ctrl.storage.ListLvmSnapshots(ctx, "sds_sdspool", "node-e", "")
	require.NoError(t, err)
	assert.Len(t, snaps, 3)
}

func TestListLvmSnapshotsRejectsAnUnknownResource(t *testing.T) {
	// Better an error than the whole pool under a heading naming a resource
	// that does not exist.
	ctrl := newBasicTestController(snapshotDeployment(realLvsOutput))
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	_, err = ctrl.storage.ListLvmSnapshots(context.Background(), "sds_sdspool", "node-e", "nope")
	require.Error(t, err)
}

func TestListLvmSnapshotsWithoutADatabaseRefusesToFilter(t *testing.T) {
	// Filtering by resource needs the volume records. Without them the honest
	// answer is an error — silently returning the whole pool under a heading
	// naming one resource is what the old code did.
	ctrl := newBasicTestController(snapshotDeployment(realLvsOutput))

	_, err := ctrl.storage.ListLvmSnapshots(context.Background(), "sds_sdspool", "node-e", "openclaw")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openclaw")
}
