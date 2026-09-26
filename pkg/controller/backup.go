package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/backup"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
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
	// gone too, this is what tells whoever is holding the bucket what these
	// files are and how big each one should be.
	manifestObject = "manifest.json"

	// manifestVersion is bumped when the on-target layout changes.
	manifestVersion = 1
)

// BackupManifest is the JSON index stored next to a backup's volume images.
type BackupManifest struct {
	Version    int                      `json:"version"`
	ID         string                   `json:"id"`
	Resource   string                   `json:"resource"`
	Node       string                   `json:"node"`
	Backend    string                   `json:"backend"`
	CreatedAt  string                   `json:"created_at"`
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

// CreateBackup ships a crash-consistent full copy of every volume of resource
// to target, and returns the recorded backup.
//
// The record only reaches "completed" once every image has been uploaded AND
// the target has confirmed it holds exactly as many bytes as were sent. A run
// that fails anywhere in between is recorded as failed and its objects are
// removed; it is never listed as something that can be restored.
//
// node selects which replica to read. Empty picks one automatically, preferring
// a Secondary so the workload's node is left alone.
func (bm *BackupManager) CreateBackup(ctx context.Context, resource, targetName, node string) (*database.Backup, error) {
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

	// Snapshot every volume before uploading any of them, so the images are as
	// close to a single instant as the storage layer allows.
	snaps, err := bm.snapshotVolumes(ctx, host, info, started)
	defer bm.removeSnapshots(context.WithoutCancel(ctx), host, info, snaps)
	if err != nil {
		return nil, err
	}

	rec := &database.Backup{
		ID: id, Resource: resource, Target: targetName, Node: node,
		Backend: bm.backend.Name(), State: database.BackupStateRunning,
		Prefix: backup.ObjectPath(resource, id), StartedAt: started,
	}
	if err := bm.controller.db.SaveBackup(ctx, rec); err != nil {
		return nil, fmt.Errorf("record backup: %w", err)
	}

	// uploaded tracks every object the node was asked to write, whether or not
	// it then verified. Cleanup has to work from this and not from rec.Volumes:
	// the volume that failed verification is precisely the one whose remains
	// are on the target, and it is the one rec.Volumes does not list.
	var uploaded []string
	if err := bm.uploadVolumes(ctx, sess, host, info, snaps, sizes, rec, &uploaded); err != nil {
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

	rec.State = database.BackupStateCompleted
	rec.FinishedAt = time.Now()
	if err := bm.controller.db.SaveBackup(ctx, rec); err != nil {
		return nil, fmt.Errorf("record completed backup: %w", err)
	}
	bm.controller.logger.Info("Backup completed",
		zap.String("backup", rec.ID), zap.String("resource", resource),
		zap.String("target", targetName), zap.Uint64("bytes", rec.TotalBytes))
	return rec, nil
}

// dataMoveTimeout is how long a volume transfer may take when the caller set no
// deadline of its own. The CLI defaults to 24h; this matches it so a backup
// started over the API is not cut shorter than one started from the shell.
const dataMoveTimeout = 24 * time.Hour

// execDataMove runs a command that streams a whole volume, bounded by the
// caller's deadline rather than by deployment.Exec's default.
//
// That default is 30 seconds. It is right for the `lvs` and `drbdsetup` queries
// Exec was written for and catastrophic for moving a volume: a 1 GiB image
// takes roughly 40 seconds on a gigabit LAN, so Exec returned while dd and
// rclone were still running. Everything downstream then read that early return
// as a finished upload — the verification found no object yet and failed the
// backup, the cleanup could not delete an object that did not exist yet, and
// the pipeline carried on regardless and eventually left a complete but
// orphaned image on the target. Every symptom traced back to this one line.
//
// It also reports a result that named no host as a failure. deployment.Exec
// returns AllSuccess() == true for an empty host set, so a command that ran
// nowhere is indistinguishable from one that succeeded — which is exactly the
// wrong default for a step whose whole purpose is moving bytes.
func (bm *BackupManager) execDataMove(ctx context.Context, host, cmd string) (*deployment.ExecResult, error) {
	timeout := dataMoveTimeout
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 {
			timeout = remaining
		}
	}
	res, err := bm.controller.deployment.Exec(ctx, []string{host}, cmd, deployment.WithExecTimeout(timeout))
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Hosts) == 0 {
		return nil, fmt.Errorf("command produced no result for %s; it may not have run at all", host)
	}
	return res, nil
}

// uploadVolumes streams each snapshot to the target and verifies the stored
// size. It stops at the first failure: half an image is not a backup.
func (bm *BackupManager) uploadVolumes(ctx context.Context, sess backup.Session, host string,
	info *ResourceInfo, snaps map[uint32]string, sizes map[uint32]uint64,
	rec *database.Backup, uploaded *[]string) error {

	for _, v := range info.Volumes {
		size := sizes[v.VolumeID]
		if size == 0 {
			return fmt.Errorf("volume %d of %q reports a zero-byte DRBD device; refusing to record an empty backup",
				v.VolumeID, info.Name)
		}
		// Compressed on the way out. A thin volume holding 1 GiB of data in a
		// 100 GiB device used to upload — and store, and bill for — all 100
		// GiB, most of it zeros. gzip is on every node SDS supports, so the
		// restoring node never lacks the tool to read it back.
		object := backup.ObjectPath(rec.Prefix, fmt.Sprintf("volume-%d.img.gz", v.VolumeID))
		snapDev := fmt.Sprintf("/dev/%s/%s", v.Pool, snaps[v.VolumeID])

		// pipefail is what makes a truncated read a failed backup: without it
		// the pipeline's exit status is rclone's alone, and rclone happily
		// stores whatever bytes reached it before dd died. The compressed
		// byte count is taken on the way through, so what the target stored
		// can still be checked against what was sent.
		cmd := fmt.Sprintf(`set -e -o pipefail; CNT=$(mktemp); trap 'rm -f "$CNT"' EXIT
sudo dd if=%s bs=4M count=%d iflag=fullblock,count_bytes status=none | gzip -1 -c | tee >(wc -c > "$CNT") | %s
for i in $(seq 1 100); do [ -s "$CNT" ] && break; sleep 0.1; done
echo "SDS_SENT=$(cat "$CNT")"`, snapDev, size, sess.PushCmd(object, size))
		*uploaded = append(*uploaded, object)
		res, err := bm.execDataMove(ctx, host, "bash -c "+shellSingleQuote(cmd))
		if err != nil {
			return fmt.Errorf("upload volume %d of %q: %w", v.VolumeID, info.Name, err)
		}
		if !res.AllSuccess() {
			return fmt.Errorf("upload volume %d of %q failed: %s", v.VolumeID, info.Name, res.FailureDetails())
		}
		sent, err := sentBytes(res)
		if err != nil {
			return fmt.Errorf("upload volume %d of %q: %w", v.VolumeID, info.Name, err)
		}

		// Ask the far end how much it actually stored. An upload command that
		// exits 0 is not evidence: this is.
		stored, err := sess.SizeBytes(ctx, object)
		if err != nil {
			return fmt.Errorf("verify volume %d of %q: %w", v.VolumeID, info.Name, err)
		}
		if stored != sent {
			return fmt.Errorf(
				"volume %d of %q uploaded short: sent %d bytes, target holds %d",
				v.VolumeID, info.Name, sent, stored)
		}

		rec.Volumes = append(rec.Volumes, database.BackupVolume{
			VolumeID: v.VolumeID, BackingVolume: v.BackingVolume, Pool: v.Pool,
			Object: object, Bytes: size,
		})
		rec.TotalBytes += size
	}
	return nil
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
		Backend: rec.Backend, CreatedAt: rec.StartedAt.UTC().Format(time.RFC3339),
		TotalBytes: rec.TotalBytes,
		Note: "Raw full images of each DRBD volume, bounded by the DRBD device size " +
			"(smaller than the backing LV, whose tail holds DRBD metadata). Restore with " +
			"`sds-cli backup restore`, or by writing each image to a device of at least that size.",
	}
	for _, v := range rec.Volumes {
		m.Volumes = append(m.Volumes, BackupManifestVolume{
			VolumeID: v.VolumeID, Object: v.Object, Bytes: v.Bytes,
			Pool: v.Pool, Backing: v.BackingVolume,
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

// pickBackupNode chooses which replica to read.
//
// An explicitly named node is honoured as given — an operator asking for the DR
// node knows what they are asking for. Otherwise a Secondary is preferred so
// the snapshot's copy-on-write cost lands away from the node serving the
// workload, and the DR node is skipped: under protocol A it may be behind, and
// a backup whose point in time is "somewhere near then" is not one.
func (bm *BackupManager) pickBackupNode(info *ResourceInfo, requested string) (string, error) {
	if requested != "" {
		for _, n := range info.Nodes {
			if n == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf("node %q holds no replica of %q", requested, info.Name)
	}

	var fallback string
	for _, n := range info.Nodes {
		if info.WANMode && n == info.DRNode {
			continue
		}
		st := info.NodeStates[n]
		if st == nil || st.DiskState != "UpToDate" {
			continue
		}
		if st.Role != "Primary" {
			return n, nil
		}
		fallback = n
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf(
		"no node holds an UpToDate replica of %q; back up from a healthy node or name one explicitly", info.Name)
}

// drbdDeviceBytes returns the size of a resource's DRBD device on host.
//
// It reads sysfs rather than running `blockdev --getsize64` on the device,
// because the node a backup reads from is usually Secondary and a Secondary
// DRBD device cannot be opened. sysfs reports the size without opening
// anything, so the same query works whatever the role happens to be.
func (bm *BackupManager) drbdDeviceBytes(ctx context.Context, host, resource string, volumeID uint32) (uint64, error) {
	cmd := fmt.Sprintf(
		`set -e; DEV=$(readlink -f /dev/drbd/by-res/%s/%d); test -n "$DEV"; cat /sys/class/block/$(basename "$DEV")/size`,
		resource, volumeID)
	res, err := bm.controller.deployment.Exec(ctx, []string{host}, cmd)
	if err != nil {
		return 0, fmt.Errorf("read DRBD device size for %s/%d on %s: %w", resource, volumeID, host, err)
	}
	if !res.AllSuccess() {
		return 0, fmt.Errorf("read DRBD device size for %s/%d on %s failed: %s",
			resource, volumeID, host, res.FailureDetails())
	}
	var out string
	if hr, ok := res.Hosts[host]; ok && hr != nil {
		out = strings.TrimSpace(hr.Output)
	}
	// sysfs reports the size in 512-byte sectors regardless of the device's
	// logical block size; that unit is part of the kernel's ABI.
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return 0, fmt.Errorf("no DRBD device size reported for %s/%d on %s", resource, volumeID, host)
	}
	sectors, perr := strconv.ParseUint(fields[len(fields)-1], 10, 64)
	if perr != nil || sectors == 0 {
		return 0, fmt.Errorf("could not read DRBD device size for %s/%d on %s (got %q)",
			resource, volumeID, host, out)
	}
	return sectors * 512, nil
}

// shellSingleQuote renders s as a single-quoted shell word.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sentBytes reads the compressed byte count an upload reports.
func sentBytes(res *deployment.ExecResult) (uint64, error) {
	for _, h := range res.Hosts {
		for _, line := range strings.Split(h.Output, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "SDS_SENT="); ok {
				n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
				if err != nil || n == 0 {
					return 0, fmt.Errorf("the upload did not report how much it sent (%q)", v)
				}
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("the upload did not report how much it sent")
}
