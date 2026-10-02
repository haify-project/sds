package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/haify-project/sds/pkg/backup"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

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
