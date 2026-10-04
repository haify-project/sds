package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/backup"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// Off-cluster backup shipping. See pkg/backup for the backend trade-off; this
// file is the orchestration: snapshot, read the snapshot, push, verify, record.
//
// Two properties drive every decision below.
//
// Crash consistency. A backup is taken from a storage-native snapshot, never
// from the live volume. Reading a mounted, actively-written volume produces an
// image whose blocks come from different instants, which is not a filesystem —
// it is something that usually mounts and occasionally does not. The snapshot
// path reused here is the same one `resource snapshot` and the GFS scheduler
// use, so there is one implementation of "freeze this volume", not two.
//
// Size. With `meta-disk internal` the DRBD device is SMALLER than the volume
// backing it: the tail of that volume holds DRBD's own metadata. A snapshot of
// the backing volume therefore contains [filesystem][DRBD metadata], and
// archiving all of it would ship one resource's DRBD metadata into another
// resource's data area on restore. Every image is bounded by the DRBD device's
// size instead, which is exactly the filesystem region. PopulateVolume carries
// the same warning for the same reason.

const (
	// backupSnapMarker separates a backing volume name from the UTC timestamp
	// in a backup snapshot's name. It is deliberately different from
	// schedSnapMarker: GFS retention must never prune a snapshot a backup is
	// mid-flight on, and a backup must never prune a scheduled one.
	backupSnapMarker = "_bk_"

	// backupIDLayout is the UTC timestamp in a backup id and snapshot name. It
	// uses only characters valid in an LVM LV name and in an object key.
	backupIDLayout = "20060102T150405Z"

	// manifestObject is the self-describing index written alongside the images.
	// It exists for the disaster the backups are for: when the SDS database is
	// gone too, `sds backup import` rebuilds the backup records, chains
	// included, from these files alone (backup_import.go).
	manifestObject = "manifest.json"

	// manifestVersion is bumped when the on-target layout changes.
	manifestVersion = 2
)

// BackupManifest is the JSON index stored next to a backup's volume images.
type BackupManifest struct {
	Version    int                      `json:"version"`
	ID         string                   `json:"id"`
	Resource   string                   `json:"resource"`
	Node       string                   `json:"node"`
	Backend    string                   `json:"backend"`
	Kind       string                   `json:"kind"`
	Parent     string                   `json:"parent,omitempty"`
	Schedule   string                   `json:"schedule,omitempty"`
	CreatedAt  string                   `json:"created_at"`
	FinishedAt string                   `json:"finished_at,omitempty"`
	TotalBytes uint64                   `json:"total_bytes"`
	Volumes    []BackupManifestVolume   `json:"volumes"`
	Note       string                   `json:"note"`
	Snapshots  []BackupManifestSnapshot `json:"snapshots"`
}

// BackupManifestVolume describes one raw image in the manifest.
type BackupManifestVolume struct {
	VolumeID uint32 `json:"volume_id"`
	Object   string `json:"object"`
	Bytes    uint64 `json:"bytes"`
	Pool     string `json:"pool"`
	Backing  string `json:"backing_volume"`
	// Ranges lists, one "offset length" pair per line, where each run of
	// bytes in an incremental Object belongs. Empty for a full image.
	Ranges string `json:"ranges,omitempty"`
	// ChangedBytes is how much of the volume an incremental image carries.
	ChangedBytes uint64 `json:"changed_bytes,omitempty"`
	// ReadThroughLUKS marks an encrypted volume's image as plaintext, read
	// through its LUKS container (backup_luks.go).
	ReadThroughLUKS bool `json:"read_through_luks,omitempty"`
}

// BackupManifestSnapshot records the snapshot each image was read from.
type BackupManifestSnapshot struct {
	VolumeID uint32 `json:"volume_id"`
	Name     string `json:"name"`
}

// BackupManager ships point-in-time copies of resources to off-cluster targets
// and restores them.
type BackupManager struct {
	controller *Controller
	backend    backup.Backend
}

// NewBackupManager creates a backup manager over the rclone backend.
func NewBackupManager(c *Controller) *BackupManager {
	return &BackupManager{controller: c, backend: backup.NewRclone()}
}

// ==================== BACKUP ====================

// ListBackups returns backup records, newest first.
func (bm *BackupManager) ListBackups(ctx context.Context, resource, target string) ([]*database.Backup, error) {
	if bm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	return bm.controller.db.ListBackups(ctx, resource, target)
}

// GetBackup returns one backup record.
func (bm *BackupManager) GetBackup(ctx context.Context, id string) (*database.Backup, error) {
	if bm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	return bm.controller.db.GetBackup(ctx, id)
}

// ReconcileInterrupted marks backups left in the "running" state as failed.
//
// Only the active controller runs backups, so on startup nothing can still be
// in flight: a record in that state belongs to a run whose controller died. It
// is turned into an explicit failure rather than left ambiguous, because the
// one thing a backup system must never do is let an incomplete copy look
// available.
func (bm *BackupManager) ReconcileInterrupted(ctx context.Context) error {
	if bm.controller.db == nil {
		return nil
	}
	backups, err := bm.controller.db.ListBackups(ctx, "", "")
	if err != nil {
		return err
	}
	for _, b := range backups {
		if b.State != database.BackupStateRunning {
			continue
		}
		b.State = database.BackupStateFailed
		b.Error = "interrupted: the controller restarted while this backup was running"
		b.FinishedAt = time.Now()
		if err := bm.controller.db.SaveBackup(ctx, b); err != nil {
			return err
		}
		bm.controller.logger.Warn("Marked interrupted backup as failed", zap.String("backup", b.ID))
	}
	return nil
}

// CreateBackup ships a crash-consistent copy of every volume of resource to
// target, and returns the recorded backup. It is incremental when the last
// backup to the same target left a base snapshot on the node it reads (see
// backup_incremental.go), and full otherwise or when full is set.
//
// The record only reaches "completed" once every image has been uploaded AND
// the target has confirmed it holds exactly as many bytes as were sent. A run
// that fails anywhere in between is recorded as failed and its objects are
// removed; it is never listed as something that can be restored.
//
// node selects which replica to read. Empty picks one automatically, preferring
// a Secondary so the workload's node is left alone.
func (bm *BackupManager) CreateBackup(ctx context.Context, resource, targetName, node string, full bool) (*database.Backup, error) {
	return bm.createBackup(ctx, resource, targetName, node, full, "")
}

// createBackup is CreateBackup on behalf of schedule, which is recorded on the
// backup so that schedule's retention can tell its own backups from others.
func (bm *BackupManager) createBackup(ctx context.Context, resource, targetName, node string, full bool, schedule string) (*database.Backup, error) {
	if bm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	dbTarget, err := bm.controller.db.GetBackupTarget(ctx, targetName)
	if err != nil {
		return nil, err
	}
	spec := targetSpecFromDB(dbTarget)

	info, err := bm.controller.resources.GetResource(ctx, resource)
	if err != nil {
		return nil, err
	}
	if len(info.Volumes) == 0 {
		return nil, fmt.Errorf("resource %q has no volumes to back up", resource)
	}
	// The backing device path comes from the database, not from ResourceInfo:
	// the live-status parse reports a volume's DRBD device (/dev/drbdN), which
	// says nothing about what is underneath it. The stored path is the backing
	// LV or zvol, which is what actually has to be snapshotted and read.
	backing, err := bm.backingDevices(ctx, resource)
	if err != nil {
		return nil, err
	}
	// An encrypted volume's snapshot is LUKS ciphertext; it is read through a
	// crypt mapping of its own so the image holds what DRBD holds.
	encrypted := bm.encryptedVolumes(ctx, resource, backing)
	for _, v := range info.Volumes {
		if isZFSDevice(backing[v.VolumeID]) {
			// A zvol snapshot has no block device unless snapdev=visible is set
			// or the snapshot is cloned first, so the read path here simply does
			// not apply. Refusing is the honest answer; a ZFS-native `zfs send`
			// pipeline is a separate change.
			return nil, fmt.Errorf(
				"resource %q volume %d is ZFS-backed; backup shipping currently supports LVM-backed volumes only",
				resource, v.VolumeID)
		}
		if v.Pool == "" || v.BackingVolume == "" {
			return nil, fmt.Errorf("resource %q volume %d has no known backing volume to snapshot", resource, v.VolumeID)
		}
	}

	// The node holding the last backup's base snapshot is the only one an
	// incremental can be read on, so it wins when it is healthy.
	if node == "" && !full {
		if last := bm.latestCompleted(ctx, resource, targetName); last != nil && bm.readable(info, last.Node) {
			node = last.Node
		}
	}
	if node == "" {
		node = bm.nodeWithSnapshotRoom(ctx, info)
	}
	node, err = bm.pickBackupNode(info, node)
	if err != nil {
		return nil, err
	}
	host := bm.controller.ResolveHost(node)
	dep := newBackupDeploymentClient(bm.controller.deployment)

	// Everything that can be checked without side effects happens before the
	// first snapshot, so a node missing rclone costs a round trip rather than a
	// snapshot that then has to be cleaned up.
	if err := bm.backend.Preflight(ctx, dep, host); err != nil {
		return nil, err
	}

	started := time.Now().UTC()
	id := resource + "_" + started.Format(backupIDLayout)
	if existing, err := bm.controller.db.GetBackup(ctx, id); err == nil && existing != nil {
		return nil, fmt.Errorf("a backup of %q with id %q already exists; retry in a second", resource, id)
	}

	// The size of each DRBD device — NOT of its backing volume. See the file
	// header for what happens if those are confused.
	sizes := make(map[uint32]uint64, len(info.Volumes))
	for _, v := range info.Volumes {
		n, err := bm.drbdDeviceBytes(ctx, host, resource, v.VolumeID)
		if err != nil {
			return nil, err
		}
		sizes[v.VolumeID] = n
	}

	sess, err := bm.backend.Prepare(ctx, dep, host, spec)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := sess.Close(context.WithoutCancel(ctx)); err != nil {
			bm.controller.logger.Warn("Failed to remove staged backup credentials",
				zap.String("node", node), zap.Error(err))
		}
	}()

	var parent *database.Backup
	if !full {
		var why string
		if parent, why = bm.incrementalBase(ctx, info, targetName, node, host, sizes, encrypted); parent == nil {
			bm.controller.logger.Info("Taking a full backup", zap.String("resource", resource), zap.String("reason", why))
		}
	}

	// Snapshot every volume before uploading any of them, so the images are as
	// close to a single instant as the storage layer allows. Snapshots kept as
	// the next incremental's base are left out of the cleanup.
	keep := map[uint32]bool{}
	snaps, err := bm.snapshotVolumes(ctx, host, info, started)
	defer func() {
		drop := make(map[uint32]string, len(snaps))
		for id, name := range snaps {
			if !keep[id] {
				drop[id] = name
			}
		}
		bm.removeSnapshots(context.WithoutCancel(ctx), host, info, drop)
	}()
	if err != nil {
		return nil, err
	}

	rec := &database.Backup{
		ID: id, Resource: resource, Target: targetName, Node: node,
		Backend: bm.backend.Name(), State: database.BackupStateRunning,
		Prefix: backup.ObjectPath(resource, id), StartedAt: started,
		Kind: database.BackupKindFull, Schedule: schedule,
	}
	if parent != nil {
		rec.Kind, rec.Parent = database.BackupKindIncremental, parent.ID
	}
	if err := bm.controller.db.SaveBackup(ctx, rec); err != nil {
		return nil, fmt.Errorf("record backup: %w", err)
	}

	// uploaded tracks every object the node was asked to write, whether or not
	// it then verified. Cleanup has to work from this and not from rec.Volumes:
	// the volume that failed verification is precisely the one whose remains
	// are on the target, and it is the one rec.Volumes does not list.
	var uploaded []string
	if parent != nil {
		err = bm.uploadDeltas(ctx, sess, host, info, parent, snaps, sizes, encrypted, rec, &uploaded)
	} else {
		err = bm.uploadVolumes(ctx, sess, host, info, snaps, sizes, encrypted, rec, &uploaded)
	}
	if err != nil {
		bm.failBackup(ctx, sess, rec, uploaded, err)
		return nil, err
	}

	manifestPath := backup.ObjectPath(rec.Prefix, manifestObject)
	manifest, err := bm.renderManifest(rec, info, snaps)
	if err != nil {
		bm.failBackup(ctx, sess, rec, uploaded, err)
		return nil, err
	}
	if err := sess.PutText(ctx, manifestPath, manifest); err != nil {
		bm.failBackup(ctx, sess, rec, append(uploaded, manifestPath), err)
		return nil, err
	}

	keep = bm.keepBases(ctx, host, rec, snaps)
	rec.State = database.BackupStateCompleted
	rec.FinishedAt = time.Now()
	if err := bm.controller.db.SaveBackup(ctx, rec); err != nil {
		return nil, fmt.Errorf("record completed backup: %w", err)
	}
	// The new snapshots are the base now; the old ones would only hold space.
	bm.releaseOlderBases(context.WithoutCancel(ctx), rec)
	bm.controller.logger.Info("Backup completed",
		zap.String("backup", rec.ID), zap.String("resource", resource),
		zap.String("target", targetName), zap.String("kind", rec.Kind), zap.Uint64("bytes", rec.TotalBytes))
	return rec, nil
}

// failBackup records the failure and removes whatever was uploaded, so a failed
// run leaves neither a restorable-looking record nor paid-for garbage.
func (bm *BackupManager) failBackup(ctx context.Context, sess backup.Session, rec *database.Backup, objects []string, cause error) {
	cleanup := context.WithoutCancel(ctx)
	for _, obj := range objects {
		if err := sess.Remove(cleanup, obj); err != nil {
			bm.controller.logger.Warn("Failed to remove object of a failed backup",
				zap.String("backup", rec.ID), zap.String("object", obj), zap.Error(err))
		}
	}
	rec.State = database.BackupStateFailed
	rec.Error = cause.Error()
	rec.FinishedAt = time.Now()
	if err := bm.controller.db.SaveBackup(cleanup, rec); err != nil {
		bm.controller.logger.Error("Failed to record backup failure",
			zap.String("backup", rec.ID), zap.Error(err))
	}
	bm.controller.logger.Warn("Backup failed",
		zap.String("backup", rec.ID), zap.String("resource", rec.Resource), zap.Error(cause))
}

// snapshotVolumes takes one storage-native snapshot per volume and returns the
// snapshot name per volume id. A partial result is returned alongside the error
// so the caller's deferred cleanup removes what did get created.
func (bm *BackupManager) snapshotVolumes(ctx context.Context, host string, info *ResourceInfo, ts time.Time) (map[uint32]string, error) {
	snaps := make(map[uint32]string, len(info.Volumes))
	dep := bm.controller.deployment
	for _, v := range info.Volumes {
		name := v.BackingVolume + backupSnapMarker + ts.Format(backupIDLayout)
		thin, err := dep.LVIsThin(ctx, host, v.Pool, v.BackingVolume)
		if err != nil {
			bm.controller.logger.Warn("Backup: LVIsThin failed; assuming thick",
				zap.String("volume", v.BackingVolume), zap.Error(err))
		}
		var res *deployment.ExecResult
		var execErr error
		if thin {
			res, execErr = dep.LVCreateThinSnapshot(ctx, []string{host}, v.Pool, v.BackingVolume, name)
		} else {
			// Thick LVM snapshots need an explicit copy-on-write reservation;
			// reuse the scheduler's sizing so both paths behave the same.
			res, execErr = dep.LVCreateSnapshot(ctx, []string{host}, v.Pool, v.BackingVolume, name, cowSize(v.SizeGB))
		}
		if execErr != nil {
			return snaps, fmt.Errorf("snapshot volume %d of %q: %w", v.VolumeID, info.Name, execErr)
		}
		if res == nil || !res.AllSuccess() {
			details := ""
			if res != nil {
				details = ": " + res.FailureDetails()
			}
			return snaps, fmt.Errorf("snapshot volume %d of %q failed on %s%s", v.VolumeID, info.Name, host, details)
		}
		snaps[v.VolumeID] = name
		// A thin snapshot is created with activation skipped and has no device
		// node to read until it is activated; see activateSnapshotCmd.
		if cmd := activateSnapshotCmd(fmt.Sprintf("/dev/%s/%s", v.Pool, name)); cmd != "" {
			if err := bm.controller.resources.execAllSuccess(ctx, []string{host}, cmd,
				fmt.Sprintf("activate the backup snapshot %s/%s", v.Pool, name)); err != nil {
				return snaps, err
			}
		}
	}
	return snaps, nil
}

// removeSnapshots drops the temporary backup snapshots. Failures are logged
// rather than returned: the backup's own outcome is already decided, and a
// leftover snapshot is a capacity problem, not a correctness one.
func (bm *BackupManager) removeSnapshots(ctx context.Context, host string, info *ResourceInfo, snaps map[uint32]string) {
	for _, v := range info.Volumes {
		name, ok := snaps[v.VolumeID]
		if !ok {
			continue
		}
		if _, err := bm.controller.deployment.LVRemoveSnapshot(ctx, []string{host}, v.Pool, name); err != nil {
			bm.controller.logger.Warn("Failed to remove backup snapshot",
				zap.String("node", host), zap.String("snapshot", name), zap.Error(err))
		}
	}
}

// renderManifest builds the JSON index stored beside the images.
func (bm *BackupManager) renderManifest(rec *database.Backup, info *ResourceInfo, snaps map[uint32]string) (string, error) {
	m := BackupManifest{
		Version: manifestVersion, ID: rec.ID, Resource: rec.Resource, Node: rec.Node,
		Backend: rec.Backend, Kind: rec.Kind, Parent: rec.Parent, Schedule: rec.Schedule,
		CreatedAt:  rec.StartedAt.UTC().Format(time.RFC3339),
		FinishedAt: time.Now().UTC().Format(time.RFC3339),
		TotalBytes: rec.TotalBytes,
		Note: "Raw full images of each DRBD volume, bounded by the DRBD device size " +
			"(smaller than the backing LV, whose tail holds DRBD metadata). Restore with " +
			"`sds backup restore`, or by writing each image to a device of at least that size. " +
			"An incremental holds only changed ranges: restore its parent chain down to the full " +
			"backup first, then write each run of its gunzipped image at the offset its ranges list gives.",
	}
	for _, v := range rec.Volumes {
		m.Volumes = append(m.Volumes, BackupManifestVolume{
			VolumeID: v.VolumeID, Object: v.Object, Bytes: v.Bytes,
			Pool: v.Pool, Backing: v.BackingVolume, Ranges: v.Ranges, ChangedBytes: v.ChangedBytes,
			ReadThroughLUKS: v.ReadThroughLUKS,
		})
	}
	for _, v := range info.Volumes {
		if name, ok := snaps[v.VolumeID]; ok {
			m.Snapshots = append(m.Snapshots, BackupManifestSnapshot{VolumeID: v.VolumeID, Name: name})
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render backup manifest: %w", err)
	}
	return string(data), nil
}

// backingDevices returns each volume's stored backing device path, keyed by
// volume id. A volume with no record maps to the empty string, which reads as
// "not ZFS" — the LVM path then fails loudly on a missing pool/backing name
// rather than quietly treating an unknown volume as something it is not.
func (bm *BackupManager) backingDevices(ctx context.Context, resource string) (map[uint32]string, error) {
	vols, err := bm.controller.db.ListVolumes(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("list volumes of %q: %w", resource, err)
	}
	out := make(map[uint32]string, len(vols))
	for _, v := range vols {
		out[uint32(v.VolumeID)] = v.Device
	}
	return out, nil
}
