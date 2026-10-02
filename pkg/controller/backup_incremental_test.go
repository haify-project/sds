package controller

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// incrementalFixture is the backup fixture with thin volumes: the snapshot
// lookup answers "thin" unless the name is in gone, and every node-side command
// is recorded so the tests can check what ran.
type incrementalFixture struct {
	ctrl    *Controller
	cmds    []string
	removed []string
	gone    map[string]bool
}

const deltaSent = uint64(4096)

func newIncrementalFixture(t *testing.T) *incrementalFixture {
	t.Helper()
	f := &incrementalFixture{gone: map[string]bool{}}
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	f.ctrl = newBackupFixture(t, stub, "Secondary")
	dep := f.ctrl.deployment.(*fakeDeploymentClient)
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		f.cmds = append(f.cmds, cmd)
		switch {
		case strings.Contains(cmd, "thin_delta"):
			return successExecResult(hosts, fmt.Sprintf("SDS_SENT=%d\nSDS_RANGES_BYTES=%d\nSDS_CHANGED=1048576\n", deltaSent, deltaSent)), nil
		case strings.Contains(cmd, "size --json") && (strings.Contains(cmd, ".delta.gz") || strings.Contains(cmd, ".ranges")):
			return successExecResult(hosts, fmt.Sprintf(`{"count":1,"bytes":%d}`, deltaSent)), nil
		}
		return stub.exec(hosts, cmd)
	}
	dep.lvIsThinFunc = func(ctx context.Context, host, vg, lv string) (bool, error) {
		return !f.gone[lv], nil
	}
	dep.lvRemoveSnapshotFunc = func(ctx context.Context, hosts []string, vg, name string) (*deployment.ExecResult, error) {
		f.removed = append(f.removed, name)
		return successExecResult(hosts, ""), nil
	}
	return f
}

// backup runs CreateBackup, waiting out the one-second id granularity so two
// backups in a row get distinct ids.
func (f *incrementalFixture) backup(t *testing.T, full bool) *database.Backup {
	t.Helper()
	if len(f.cmds) > 0 {
		time.Sleep(1100 * time.Millisecond)
	}
	rec, err := f.ctrl.backups.CreateBackup(context.Background(), "data", "offsite", "", full)
	require.NoError(t, err)
	return rec
}

func (f *incrementalFixture) ran(substr string) []string {
	var out []string
	for _, c := range f.cmds {
		if strings.Contains(c, substr) {
			out = append(out, c)
		}
	}
	return out
}

func TestSecondBackupShipsOnlyWhatChanged(t *testing.T) {
	f := newIncrementalFixture(t)
	first := f.backup(t, false)
	assert.Equal(t, database.BackupKindFull, first.Kind)
	require.NotEmpty(t, first.Volumes[0].Snapshot, "a thin backup keeps its snapshot as the next base")

	second := f.backup(t, false)
	assert.Equal(t, database.BackupKindIncremental, second.Kind)
	assert.Equal(t, first.ID, second.Parent)
	assert.Equal(t, uint64(1048576), second.Volumes[0].ChangedBytes)
	assert.NotEmpty(t, second.Volumes[0].Ranges)

	deltas := f.ran("thin_delta")
	require.Len(t, deltas, 1)
	assert.Contains(t, deltas[0], first.Volumes[0].Snapshot, "the delta is computed against the last backup's base")
	assert.Contains(t, deltas[0], "reserve_metadata_snap")
	assert.Contains(t, deltas[0], "release_metadata_snap")

	// The new snapshot is the base now; the old one only holds space.
	assert.Contains(t, f.removed, first.Volumes[0].Snapshot)
	assert.NotContains(t, f.removed, second.Volumes[0].Snapshot)
	old, err := f.ctrl.db.GetBackup(context.Background(), first.ID)
	require.NoError(t, err)
	assert.Empty(t, old.Volumes[0].Snapshot)
}

func TestFullFlagStartsANewChain(t *testing.T) {
	f := newIncrementalFixture(t)
	f.backup(t, false)
	rec := f.backup(t, true)
	assert.Equal(t, database.BackupKindFull, rec.Kind)
	assert.Empty(t, rec.Parent)
	assert.Empty(t, f.ran("thin_delta"))
}

// A base that vanished — removed by hand, or the resource was recreated — must
// produce a full backup, never a delta against nothing.
func TestMissingBaseFallsBackToFull(t *testing.T) {
	f := newIncrementalFixture(t)
	first := f.backup(t, false)
	f.gone[first.Volumes[0].Snapshot] = true
	rec := f.backup(t, false)
	assert.Equal(t, database.BackupKindFull, rec.Kind)
	assert.Empty(t, f.ran("thin_delta"))
}

func TestThickVolumesKeepNoBase(t *testing.T) {
	f := newIncrementalFixture(t)
	f.ctrl.deployment.(*fakeDeploymentClient).lvIsThinFunc = func(context.Context, string, string, string) (bool, error) {
		return false, nil
	}
	rec := f.backup(t, false)
	assert.Empty(t, rec.Volumes[0].Snapshot, "a thick snapshot slows every write to its origin; it is not kept")
}

func TestABaseCannotBeDeletedUnderItsIncrementals(t *testing.T) {
	f := newIncrementalFixture(t)
	first := f.backup(t, false)
	second := f.backup(t, false)

	err := f.ctrl.backups.DeleteBackup(context.Background(), first.ID, "", true)
	require.Error(t, err, "--force must not orphan a later incremental")
	assert.Contains(t, err.Error(), second.ID)

	require.NoError(t, f.ctrl.backups.DeleteBackup(context.Background(), second.ID, "", false))
	assert.Contains(t, f.removed, second.Volumes[0].Snapshot, "deleting the newest backup drops its base")
	require.NoError(t, f.ctrl.backups.DeleteBackup(context.Background(), first.ID, "", false))
}

func TestRestoringAnIncrementalReplaysTheChainOldestFirst(t *testing.T) {
	f := newIncrementalFixture(t)
	first := f.backup(t, false)
	second := f.backup(t, false)
	third := f.backup(t, false)
	require.Equal(t, second.ID, third.Parent)

	f.cmds = nil
	_, err := f.ctrl.backups.RestoreBackup(context.Background(), third.ID, "", "")
	require.NoError(t, err)

	var order []string
	for _, c := range f.cmds {
		switch {
		case strings.Contains(c, "volume-0.img.gz"):
			order = append(order, "full:"+first.ID)
		case strings.Contains(c, "seek_bytes"):
			for _, b := range []*database.Backup{second, third} {
				if strings.Contains(c, b.ID) {
					order = append(order, "delta:"+b.ID)
				}
			}
		}
	}
	assert.Equal(t, []string{"full:" + first.ID, "delta:" + second.ID, "delta:" + third.ID}, order)
}

// The awk program is the part that decides which bytes are shipped, so it is
// run for real against thin_delta output captured on a node: 64 KiB blocks,
// one discarded range, one rewritten range 4 blocks later, one new range far
// away.
func TestThinDeltaRangesAreClippedAndMerged(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk")
	}
	xml := `<superblock uuid="" time="2" transaction="3" data_block_size="128" nr_data_blocks="0">
  <diff left="2" right="3">
    <same begin="0" length="80"/>
    <left_only begin="80" length="16"/>
    <same begin="96" length="4"/>
    <different begin="100" length="3"/>
    <same begin="103" length="217"/>
    <right_only begin="800" length="32"/>
  </diff>
</superblock>`
	run := func(size uint64) string {
		cmd := exec.Command("awk", "-v", fmt.Sprintf("size=%d", size), "-v", fmt.Sprintf("gap=%d", deltaCoalesceGap), thinDeltaAwk)
		cmd.Stdin = strings.NewReader(xml)
		out, err := cmd.Output()
		require.NoError(t, err)
		return string(out)
	}
	// The discard and the rewrite 256 KiB later become one read.
	assert.Equal(t, "5242880 1507328\n52428800 2097152\n", run(100<<20))
	// The tail past the DRBD device is its metadata, never shipped.
	assert.Equal(t, "5242880 1507328\n52428800 571200\n", run(53000000))
	assert.Equal(t, "5242880 1507328\n", run(52428800))
}
