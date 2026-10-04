package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/backup"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// Backup shipping is the layer that has to be right when everything else has
// already gone wrong, so these tests care far more about the refusals than
// about the happy path: a backup that is recorded as usable and is not is
// worse than no backup feature at all.

const backupVolumeBytes = uint64(4 * 1024 * 1024 * 1024)

// drbdStatusFor renders the `drbdadm status --verbose` output the resource
// parser consumes, for a two-node resource with both replicas UpToDate.
func drbdStatusFor(resource, role string) string {
	return fmt.Sprintf(""+
		"%s node-id:0 role:%s suspended:no force-io-failures:no\n"+
		"  volume:0 minor:0 disk:UpToDate backing_dev:/dev/vg0/%s_data quorum:yes\n"+
		"      open:no blocked:no\n"+
		"  peer node-id:1 connection:Connected role:Secondary tls:no congested:no\n"+
		"    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no\n",
		resource, role, resource)
}

// backupExecStub answers the node-side commands a backup issues. storedBytes is
// what the target claims to hold after an upload, which is the knob the
// short-upload tests turn.
type backupExecStub struct {
	storedBytes uint64
	uploadFails bool
	sizeFails   bool
	deletes     []string
	uploads     []string
}

func (s *backupExecStub) exec(hosts []string, cmd string) (*deployment.ExecResult, error) {
	switch {
	case strings.Contains(cmd, "size --json"):
		if s.sizeFails {
			return failedExecResult(hosts, "directory not found"), nil
		}
		return successExecResult(hosts, fmt.Sprintf(`{"count":1,"bytes":%d}`, s.storedBytes)), nil
	case strings.Contains(cmd, "deletefile"):
		s.deletes = append(s.deletes, cmd)
		return successExecResult(hosts, ""), nil
	case strings.Contains(cmd, "rcat"):
		// The manifest goes through rcat too; only the dd pipeline is an image.
		if strings.Contains(cmd, "sudo dd if=") {
			s.uploads = append(s.uploads, cmd)
		}
		if s.uploadFails {
			return failedExecResult(hosts, "dd: reading '/dev/vg0/...': Input/output error"), nil
		}
		if strings.Contains(cmd, "sudo dd if=") {
			// The image pipeline reports the compressed bytes it sent; the
			// fixture's target stores storedBytes of them.
			return successExecResult(hosts, fmt.Sprintf("SDS_SENT=%d\n", backupVolumeBytes)), nil
		}
		return successExecResult(hosts, ""), nil
	case strings.Contains(cmd, "/sys/class/block/"):
		return successExecResult(hosts, fmt.Sprintf("%d\n", backupVolumeBytes/512)), nil
	default:
		return successExecResult(hosts, ""), nil
	}
}

func failedExecResult(hosts []string, out string) *deployment.ExecResult {
	r := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
	for _, h := range hosts {
		r.Hosts[h] = &deployment.HostResult{Host: h, Output: out, Success: false}
	}
	return r
}

// newBackupFixture builds a controller with one two-node LVM resource, one S3
// target, and a deployment fake wired to stub.
func newBackupFixture(t *testing.T, stub *backupExecStub, role string) *Controller {
	t.Helper()
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			return stub.exec(hosts, cmd)
		},
		drbdStatusFunc: func(ctx context.Context, hosts []string, resource string) (*deployment.ExecResult, error) {
			return successExecResult(hosts, drbdStatusFor(resource, role)), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)

	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{
		Name: "data", Port: 7000, Nodes: "10.0.0.1,10.0.0.2", Protocol: "C", Replicas: 2,
	}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{
		ResourceName: "data", VolumeName: "data_data", VolumeID: 0, Pool: "vg0", SizeGB: 4,
	}))
	require.NoError(t, ctrl.backups.AddTarget(ctx, backup.TargetSpec{
		Name: "offsite", Kind: backup.KindS3, Bucket: "b", User: "k", Secret: "s3cr3t",
	}))
	return ctrl
}

func TestBackupCreateRecordsACompletedBackup(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")

	rec, err := ctrl.backups.CreateBackup(context.Background(), "data", "offsite", "", false)
	require.NoError(t, err)

	assert.Equal(t, database.BackupStateCompleted, rec.State)
	assert.Equal(t, backupVolumeBytes, rec.TotalBytes)
	require.Len(t, rec.Volumes, 1)
	assert.Equal(t, "data/"+rec.ID+"/volume-0.img.gz", rec.Volumes[0].Object)
	assert.Contains(t, stub.uploads[0], "gzip -1 -c", "images are compressed on the way out")

	// The image must be bounded by the DRBD device's size, not by the backing
	// LV's: with meta-disk internal the LV is larger, and its tail is DRBD
	// metadata that has no business inside a filesystem image.
	require.Len(t, stub.uploads, 1)
	assert.Contains(t, stub.uploads[0], fmt.Sprintf("count=%d", backupVolumeBytes))
	assert.Contains(t, stub.uploads[0], "iflag=fullblock,count_bytes")

	// And it must come from a snapshot, never from the live volume.
	assert.Contains(t, stub.uploads[0], "SRC='\\''/dev/vg0/data_data"+backupSnapMarker)
	assert.Contains(t, stub.uploads[0], `dd if="$SRC"`)
	assert.NotContains(t, stub.uploads[0], "cryptsetup", "an unencrypted volume is read as it is")

	// pipefail is what turns a dd that dies mid-stream into a failed upload
	// instead of a truncated object stored successfully.
	assert.Contains(t, stub.uploads[0], "set -e -o pipefail")
}

// The central guarantee. If the target ends up holding fewer bytes than were
// sent, that is a partial upload, and it must never be recorded as a backup
// anyone could restore from.
func TestBackupShortUploadIsNeverRecordedAsSuccess(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes - 4096}
	ctrl := newBackupFixture(t, stub, "Secondary")
	ctx := context.Background()

	_, err := ctrl.backups.CreateBackup(ctx, "data", "offsite", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uploaded short")

	backups, err := ctrl.backups.ListBackups(ctx, "data", "")
	require.NoError(t, err)
	require.Len(t, backups, 1, "the attempt must still be visible")
	assert.Equal(t, database.BackupStateFailed, backups[0].State)
	assert.Contains(t, backups[0].Error, "uploaded short")

	// The half-written object is paid-for garbage; it must be cleaned up.
	assert.NotEmpty(t, stub.deletes, "a failed backup must remove what it uploaded")
}

func TestBackupFailedUploadCommandIsRecordedAsFailed(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes, uploadFails: true}
	ctrl := newBackupFixture(t, stub, "Secondary")
	ctx := context.Background()

	_, err := ctrl.backups.CreateBackup(ctx, "data", "offsite", "", false)
	require.Error(t, err)

	backups, err := ctrl.backups.ListBackups(ctx, "data", "")
	require.NoError(t, err)
	require.Len(t, backups, 1)
	assert.Equal(t, database.BackupStateFailed, backups[0].State)
}

// An upload that "succeeded" but whose size cannot be read is unverified, and
// unverified is not completed.
func TestBackupUnverifiableUploadIsRecordedAsFailed(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes, sizeFails: true}
	ctrl := newBackupFixture(t, stub, "Secondary")
	ctx := context.Background()

	_, err := ctrl.backups.CreateBackup(ctx, "data", "offsite", "", false)
	require.Error(t, err)

	backups, err := ctrl.backups.ListBackups(ctx, "data", "")
	require.NoError(t, err)
	require.Len(t, backups, 1)
	assert.Equal(t, database.BackupStateFailed, backups[0].State)
}

// A missing rclone must be found before a snapshot exists, not after: the
// alternative is a snapshot filling a pool while the operator works out why
// the backup failed.
func TestBackupPreflightFailsBeforeAnySnapshot(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	ctrl.deployment.(*fakeDeploymentClient).execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "command -v rclone") {
			return failedExecResult(hosts, "rclone is not installed"), nil
		}
		return stub.exec(hosts, cmd)
	}

	_, err := ctrl.backups.CreateBackup(context.Background(), "data", "offsite", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rclone is required")

	backups, err := ctrl.backups.ListBackups(context.Background(), "", "")
	require.NoError(t, err)
	assert.Empty(t, backups, "nothing should be recorded when preflight fails")
}

// A zvol snapshot has no block device to read unless it is cloned or made
// visible first, so the read path here simply does not apply. Refusing beats
// producing an image of whatever /dev path happened to exist.
func TestBackupRefusesZFSBackedVolumes(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	ctx := context.Background()

	// Re-point the volume at a zvol. The DRBD status the fixture serves is
	// unchanged, which is the realistic case: live status reports /dev/drbdN
	// whatever is underneath, so only the stored backing path can tell ZFS
	// from LVM.
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{
		ResourceName: "data", VolumeName: "data_data", VolumeID: 0, Pool: "tank",
		SizeGB: 4, Device: "/dev/zvol/tank/data_data",
	}))

	_, err := ctrl.backups.CreateBackup(ctx, "data", "offsite", "", false)
	require.Error(t, err, "a ZFS-backed resource must be refused, not half-supported")
	assert.Contains(t, err.Error(), "ZFS-backed")
}

// ==================== RESTORE ====================

func completedBackup(t *testing.T, ctrl *Controller) *database.Backup {
	t.Helper()
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	_ = stub
	rec := &database.Backup{
		ID: "data_20260101T000000Z", Resource: "data", Target: "offsite", Node: "10.0.0.1",
		Backend: "rclone", State: database.BackupStateCompleted, Prefix: "data/data_20260101T000000Z",
		TotalBytes: backupVolumeBytes,
		Volumes: []database.BackupVolume{{
			VolumeID: 0, BackingVolume: "data_data", Pool: "vg0",
			Object: "data/data_20260101T000000Z/volume-0.img", Bytes: backupVolumeBytes,
		}},
	}
	require.NoError(t, ctrl.db.SaveBackup(context.Background(), rec))
	return rec
}

// DRBD's own rule defines "in use": only a Primary can be open. Restoring over
// a Primary would overwrite a mounted filesystem from underneath its kernel.
func TestRestoreRefusesAResourceThatIsPrimary(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Primary")
	completedBackup(t, ctrl)

	_, err := ctrl.backups.RestoreBackup(context.Background(), "data_20260101T000000Z", "data", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in use")
}

// Nothing is Primary right now, but a gateway's promoter can make it Primary at
// any moment — including halfway through the restore.
func TestRestoreRefusesAResourceExportedByAGateway(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	completedBackup(t, ctrl)
	require.NoError(t, ctrl.db.SaveGateway(context.Background(), &database.Gateway{
		Name: "data", Resource: "data", Type: database.GatewayTypeNFS,
	}))

	_, err := ctrl.backups.RestoreBackup(context.Background(), "data_20260101T000000Z", "data", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gateway")
}

func TestRestoreRefusesABackupThatIsNotCompleted(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	rec := completedBackup(t, ctrl)
	rec.State = database.BackupStateRunning
	require.NoError(t, ctrl.db.SaveBackup(context.Background(), rec))

	_, err := ctrl.backups.RestoreBackup(context.Background(), rec.ID, "data", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be restored")
}

// A destination smaller than the image would take a silently truncated copy: a
// filesystem that mounts and is missing its tail. Refuse before promoting
// anything.
func TestRestoreRefusesATooSmallDestination(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	rec := completedBackup(t, ctrl)
	rec.Volumes[0].Bytes = backupVolumeBytes * 2
	require.NoError(t, ctrl.db.SaveBackup(context.Background(), rec))

	_, err := ctrl.backups.RestoreBackup(context.Background(), rec.ID, "data", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least as large")
}

func TestRestoreWritesThroughTheDRBDDeviceBoundedByTheImage(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	rec := completedBackup(t, ctrl)

	var restoreCmd string
	dep := ctrl.deployment.(*fakeDeploymentClient)
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "rclone") && strings.Contains(cmd, " cat ") {
			restoreCmd = cmd
		}
		return stub.exec(hosts, cmd)
	}

	_, err := ctrl.backups.RestoreBackup(context.Background(), rec.ID, "data", "10.0.0.1")
	require.NoError(t, err)

	require.NotEmpty(t, restoreCmd, "the restore must stream from the target")
	// Writing to the DRBD device rather than the backing LV is what makes DRBD
	// replicate the restore to the peers as part of the write path.
	assert.Contains(t, restoreCmd, "of=/dev/drbd/by-res/data/0")
	assert.Contains(t, restoreCmd, fmt.Sprintf("count=%d", backupVolumeBytes))
	// The fixture's replicas are not thin, so every byte is written.
	assert.Contains(t, restoreCmd, "CONV=fsync; ")
	assert.Contains(t, restoreCmd, "conv=$CONV")
	assert.NotContains(t, restoreCmd, "sparse")
	assert.Contains(t, restoreCmd, "set -e -o pipefail")
}

// ==================== LIFECYCLE ====================

func TestInterruptedBackupsAreResolvedOnStartup(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveBackup(ctx, &database.Backup{
		ID: "data_x", Resource: "data", Target: "offsite", State: database.BackupStateRunning,
	}))

	require.NoError(t, ctrl.backups.ReconcileInterrupted(ctx))

	got, err := ctrl.db.GetBackup(ctx, "data_x")
	require.NoError(t, err)
	assert.Equal(t, database.BackupStateFailed, got.State)
	assert.Contains(t, got.Error, "controller restarted")
}

func TestDeleteTargetRefusesWhileBackupsReferenceIt(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	completedBackup(t, ctrl)
	ctx := context.Background()

	err := ctrl.backups.DeleteTarget(ctx, "offsite", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")

	require.NoError(t, ctrl.backups.DeleteTarget(ctx, "offsite", true))
}

// A credential that can be read back out of the API leaks through every client
// that ever renders it.
func TestListTargetsNeverReturnsTheSecret(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")

	targets, err := ctrl.backups.ListTargets(context.Background())
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Empty(t, targets[0].Secret)
	assert.Equal(t, "k", targets[0].User, "the non-secret half is still useful to show")
}

func TestDeleteBackupKeepsTheRecordWhenObjectsSurvive(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	rec := completedBackup(t, ctrl)
	ctx := context.Background()

	dep := ctrl.deployment.(*fakeDeploymentClient)
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "deletefile") || strings.Contains(cmd, "lsf") {
			return failedExecResult(hosts, "AccessDenied"), nil
		}
		return stub.exec(hosts, cmd)
	}

	err := ctrl.backups.DeleteBackup(ctx, rec.ID, "", false)
	require.Error(t, err)
	_, err = ctrl.db.GetBackup(ctx, rec.ID)
	require.NoError(t, err, "the record must survive so the deletion stays retryable")

	require.NoError(t, ctrl.backups.DeleteBackup(ctx, rec.ID, "", true))
	_, err = ctrl.db.GetBackup(ctx, rec.ID)
	require.Error(t, err)
}

// ==================== NODE SELECTION ====================

func TestPickBackupNodePrefersAnUpToDateSecondary(t *testing.T) {
	bm := &BackupManager{}
	info := &ResourceInfo{
		Name: "data", Nodes: []string{"a", "b"},
		NodeStates: map[string]*ResourceNodeState{
			"a": {Role: "Primary", DiskState: "UpToDate"},
			"b": {Role: "Secondary", DiskState: "UpToDate"},
		},
	}
	node, err := bm.pickBackupNode(info, "")
	require.NoError(t, err)
	assert.Equal(t, "b", node, "the snapshot's copy-on-write cost belongs away from the workload")
}

// The DR replica is asynchronous, so its contents lag by an unknown amount. A
// backup whose point in time is "somewhere near then" is not a backup.
func TestPickBackupNodeSkipsTheDRReplica(t *testing.T) {
	bm := &BackupManager{}
	info := &ResourceInfo{
		Name: "data", Nodes: []string{"a", "dr"}, WANMode: true, DRNode: "dr",
		NodeStates: map[string]*ResourceNodeState{
			"a":  {Role: "Primary", DiskState: "UpToDate"},
			"dr": {Role: "Secondary", DiskState: "UpToDate"},
		},
	}
	node, err := bm.pickBackupNode(info, "")
	require.NoError(t, err)
	assert.Equal(t, "a", node)

	// Unless it is asked for explicitly: an operator naming the DR node knows
	// what they are asking for.
	node, err = bm.pickBackupNode(info, "dr")
	require.NoError(t, err)
	assert.Equal(t, "dr", node)
}

func TestPickBackupNodeRefusesWhenNothingIsUpToDate(t *testing.T) {
	bm := &BackupManager{}
	_, err := bm.pickBackupNode(&ResourceInfo{
		Name: "data", Nodes: []string{"a"},
		NodeStates: map[string]*ResourceNodeState{"a": {Role: "Secondary", DiskState: "Inconsistent"}},
	}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UpToDate")
}
