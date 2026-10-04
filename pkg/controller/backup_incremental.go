package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/backup"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// Incremental backups. A full image of a 2 TiB volume every night is 2 TiB on
// the wire every night; an incremental carries only the blocks that changed.
//
// What changed is not guessed by reading and hashing the volume: the thin pool
// already knows. After a backup its snapshot is kept on the node as a base, and
// the next backup asks thin_delta which pool blocks differ between that base
// and the new snapshot. Only those ranges are read and shipped. The cost of
// this is one retained snapshot per resource and target, which holds whatever
// the volume has overwritten since — the same space a scheduled snapshot of
// that age would hold.
//
// A chain is restored by writing the full image and then applying each
// incremental in order, so every link has to stay on the target: a backup
// another one is built on cannot be deleted, and a chain is capped so a restore
// never has to replay an unbounded history.

// maxIncrementalChain is how many incrementals may follow a full backup before
// the next one is taken full again.
const maxIncrementalChain = 30

// deltaCoalesceGap merges changed ranges separated by less than this much
// unchanged data. Every range is its own read on the node and its own write on
// restore, so a volume with scattered small writes would otherwise cost a dd
// per 64 KiB; re-sending a little unchanged data is far cheaper.
const deltaCoalesceGap = 1 << 20

// incrementalBase finds the backup the next one can be computed against, or
// returns the reason a full backup is needed instead. host is where the new
// backup will be read; encrypted names the volumes that are LUKS containers.
func (bm *BackupManager) incrementalBase(ctx context.Context, info *ResourceInfo, target, node, host string,
	sizes map[uint32]uint64, encrypted map[uint32]bool) (*database.Backup, string) {

	parent := bm.latestCompleted(ctx, info.Name, target)
	if parent == nil {
		return nil, "no earlier backup of this resource on this target"
	}
	if parent.Node != node {
		return nil, fmt.Sprintf("the last backup was read on %s, this one on %s", parent.Node, node)
	}
	if n := bm.chainLength(ctx, parent); n >= maxIncrementalChain {
		return nil, fmt.Sprintf("the chain already holds %d incrementals", n)
	}
	byID := make(map[uint32]database.BackupVolume, len(parent.Volumes))
	for _, v := range parent.Volumes {
		byID[v.VolumeID] = v
	}
	for _, v := range info.Volumes {
		pv, ok := byID[v.VolumeID]
		switch {
		case !ok:
			return nil, fmt.Sprintf("volume %d is not in the last backup", v.VolumeID)
		case pv.Snapshot == "":
			return nil, fmt.Sprintf("the last backup kept no base snapshot of volume %d", v.VolumeID)
		case pv.Pool != v.Pool || pv.BackingVolume != v.BackingVolume:
			return nil, fmt.Sprintf("volume %d is backed by a different volume than in the last backup", v.VolumeID)
		case pv.Bytes != sizes[v.VolumeID]:
			return nil, fmt.Sprintf("volume %d changed size since the last backup", v.VolumeID)
		case encrypted[v.VolumeID] && !pv.ReadThroughLUKS:
			// Changes on top of a ciphertext image would restore to noise.
			return nil, fmt.Sprintf("the last backup of encrypted volume %d holds its ciphertext, not its data", v.VolumeID)
		}
		thin, err := bm.controller.deployment.LVIsThin(ctx, host, pv.Pool, pv.Snapshot)
		if err != nil || !thin {
			return nil, fmt.Sprintf("the base snapshot %s/%s is gone from %s", pv.Pool, pv.Snapshot, node)
		}
	}
	return parent, ""
}

// latestCompleted is the newest completed backup of resource on target.
func (bm *BackupManager) latestCompleted(ctx context.Context, resource, target string) *database.Backup {
	backups, err := bm.controller.db.ListBackups(ctx, resource, target)
	if err != nil {
		return nil
	}
	for _, b := range backups {
		if b.State == database.BackupStateCompleted {
			return b
		}
	}
	return nil
}

// chainLength counts the incrementals from rec down to its full backup.
func (bm *BackupManager) chainLength(ctx context.Context, rec *database.Backup) int {
	n := 0
	for rec != nil && rec.Kind == database.BackupKindIncremental && n <= maxIncrementalChain {
		n++
		parent, err := bm.controller.db.GetBackup(ctx, rec.Parent)
		if err != nil {
			break
		}
		rec = parent
	}
	return n
}

// restoreChain returns the backups to apply to restore rec, full backup first.
func (bm *BackupManager) restoreChain(ctx context.Context, rec *database.Backup) ([]*database.Backup, error) {
	chain := []*database.Backup{rec}
	for cur := rec; cur.Kind == database.BackupKindIncremental; {
		if len(chain) > maxIncrementalChain+1 {
			return nil, fmt.Errorf("backup %q: the chain to its full backup is longer than %d", rec.ID, maxIncrementalChain)
		}
		parent, err := bm.controller.db.GetBackup(ctx, cur.Parent)
		if err != nil {
			return nil, fmt.Errorf("backup %q is built on %q, which is gone: %w", cur.ID, cur.Parent, err)
		}
		if parent.State != database.BackupStateCompleted {
			return nil, fmt.Errorf("backup %q is built on %q, which is %s", cur.ID, parent.ID, parent.State)
		}
		chain = append(chain, parent)
		cur = parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// uploadDeltas ships, for every volume, the ranges that differ between the
// parent's base snapshot and the new one.
func (bm *BackupManager) uploadDeltas(ctx context.Context, sess backup.Session, host string,
	info *ResourceInfo, parent *database.Backup, snaps map[uint32]string, sizes map[uint32]uint64,
	encrypted map[uint32]bool, rec *database.Backup, uploaded *[]string) error {

	bases := make(map[uint32]string, len(parent.Volumes))
	for _, v := range parent.Volumes {
		bases[v.VolumeID] = v.Snapshot
	}
	for _, v := range info.Volumes {
		size := sizes[v.VolumeID]
		ranges := backup.ObjectPath(rec.Prefix, fmt.Sprintf("volume-%d.ranges", v.VolumeID))
		object := backup.ObjectPath(rec.Prefix, fmt.Sprintf("volume-%d.delta.gz", v.VolumeID))
		source, closeSource, err := backupSnapshotSource(v.Pool, v.BackingVolume, snaps[v.VolumeID], encrypted[v.VolumeID])
		if err != nil {
			return fmt.Errorf("volume %d of %q: %w", v.VolumeID, info.Name, err)
		}
		cmd := deltaUploadCmd(v.Pool, bases[v.VolumeID], snaps[v.VolumeID], size, source, closeSource,
			sess.PushCmd(ranges, 0), sess.PushCmd(object, size))
		*uploaded = append(*uploaded, ranges, object)
		res, err := bm.execDataMove(ctx, host, "bash -c "+shellSingleQuote(cmd))
		if err != nil {
			return fmt.Errorf("upload changes to volume %d of %q: %w", v.VolumeID, info.Name, err)
		}
		if !res.AllSuccess() {
			return fmt.Errorf("upload changes to volume %d of %q failed: %s", v.VolumeID, info.Name, res.FailureDetails())
		}
		report, err := parseDeltaReport(res)
		if err != nil {
			return fmt.Errorf("upload changes to volume %d of %q: %w", v.VolumeID, info.Name, err)
		}
		for obj, want := range map[string]uint64{ranges: report.rangesBytes, object: report.sent} {
			stored, err := sess.SizeBytes(ctx, obj)
			if err != nil {
				return fmt.Errorf("verify volume %d of %q: %w", v.VolumeID, info.Name, err)
			}
			if stored != want {
				return fmt.Errorf("volume %d of %q uploaded short: sent %d bytes of %s, target holds %d",
					v.VolumeID, info.Name, want, obj, stored)
			}
		}
		rec.Volumes = append(rec.Volumes, database.BackupVolume{
			VolumeID: v.VolumeID, BackingVolume: v.BackingVolume, Pool: v.Pool,
			Object: object, Ranges: ranges, Bytes: size, ChangedBytes: report.changed,
			ReadThroughLUKS: encrypted[v.VolumeID],
		})
		rec.TotalBytes += size
	}
	return nil
}

// deltaUploadCmd asks the thin pool which blocks differ between base and snap,
// clips them to the DRBD device (the tail of the LV is DRBD's metadata),
// merges near neighbours, and uploads the list and then the data in that order.
//
// thin_delta reads the pool's metadata, which the kernel is changing under it,
// so it runs against a metadata snapshot that is reserved for the duration and
// released whatever happens. Blocks mapped only in base (discarded since) are
// shipped like changed ones: reading them from the new snapshot yields zeros,
// which is exactly what a restore must write there.
//
// The data is read from the device source sets up (see backupSnapshotSource):
// thin_delta's ranges address the LV, and the awk shifts them by $OFF so they
// address the same bytes of $SRC — on an encrypted volume, the plaintext
// behind the LUKS header. Ranges inside the header itself are dropped. A
// discarded block there reads as whatever its zeroed ciphertext decrypts to,
// not as zeros — which is also what the live volume reads, so the restore
// still reproduces it exactly.
func deltaUploadCmd(vg, base, snap string, size uint64, source, closeSource, pushRanges, pushData string) string {
	return fmt.Sprintf(`set -e -o pipefail
VG=%s; BASE=%s; NEW=%s; SIZE=%d
R=$(mktemp); CNT=$(mktemp); DM=; MAP=
release() { [ -n "$DM" ] && sudo dmsetup message "$DM-tpool" 0 release_metadata_snap >/dev/null 2>&1; %s; rm -f "$R" "$CNT"; }
trap release EXIT
%s
POOL=$(sudo lvs --noheadings -o pool_lv "$VG/$NEW" | tr -d ' ')
I1=$(sudo lvs --noheadings -o thin_id "$VG/$BASE" | tr -d ' ')
I2=$(sudo lvs --noheadings -o thin_id "$VG/$NEW" | tr -d ' ')
DM=$(echo "$VG" | sed 's/-/--/g')-$(echo "$POOL" | sed 's/-/--/g')
sudo dmsetup message "$DM-tpool" 0 reserve_metadata_snap
sudo thin_delta -m --snap1 "$I1" --snap2 "$I2" "/dev/mapper/${DM}_tmeta" | awk -v size="$SIZE" -v off="$OFF" -v gap=%d '%s' > "$R"
sudo dmsetup message "$DM-tpool" 0 release_metadata_snap; DM=
%s < "$R"
while read -r -u3 off len; do
  sudo dd if="$SRC" bs=1M iflag=skip_bytes,count_bytes,fullblock skip="$off" count="$len" status=none
done 3<"$R" | gzip -1 -c | tee >(wc -c > "$CNT") | %s
for i in $(seq 1 100); do [ -s "$CNT" ] && break; sleep 0.1; done
echo "SDS_SENT=$(cat "$CNT")"
echo "SDS_RANGES_BYTES=$(wc -c < "$R")"
echo "SDS_CHANGED=$(awk '{s+=$2} END {printf "%%.0f", s}' "$R")"`,
		shellSingleQuote(vg), shellSingleQuote(base), shellSingleQuote(snap), size,
		closeSource, source, deltaCoalesceGap, thinDeltaAwk, pushRanges, pushData)
}

// thinDeltaAwk turns thin_delta's XML into "offset length" byte ranges. Blocks
// are data_block_size sectors; printf keeps offsets past 2^31 from being
// printed in exponent form, which mawk does with print. off (0 when unset) is
// subtracted from every range first: the bytes before it are a LUKS header.
const thinDeltaAwk = `/data_block_size=/ { if (match($0, /data_block_size="[0-9]+"/)) bs = substr($0, RSTART+17, RLENGTH-18) * 512 }
/<(different|right_only|left_only) / {
  match($0, /begin="[0-9]+"/); b = substr($0, RSTART+7, RLENGTH-8) * bs
  match($0, /length="[0-9]+"/); l = substr($0, RSTART+8, RLENGTH-9) * bs
  b -= off; if (b + l <= 0) next; if (b < 0) { l += b; b = 0 }
  if (b >= size) next
  if (b + l > size) l = size - b
  if (have && b <= cs + cl + gap) { if (b + l > cs + cl) cl = b + l - cs; next }
  if (have) printf "%.0f %.0f\n", cs, cl
  cs = b; cl = l; have = 1
}
END { if (bs == 0) exit 3; if (have) printf "%.0f %.0f\n", cs, cl }`

type deltaReport struct {
	sent, rangesBytes, changed uint64
}

// parseDeltaReport reads the counters deltaUploadCmd prints.
func parseDeltaReport(res *deployment.ExecResult) (deltaReport, error) {
	var r deltaReport
	seen := map[string]bool{}
	for _, h := range res.Hosts {
		for _, line := range strings.Split(h.Output, "\n") {
			key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			n, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
			if err != nil {
				continue
			}
			switch key {
			case "SDS_SENT":
				r.sent = n
			case "SDS_RANGES_BYTES":
				r.rangesBytes = n
			case "SDS_CHANGED":
				r.changed = n
			default:
				continue
			}
			seen[key] = true
		}
	}
	if len(seen) != 3 || r.sent == 0 {
		return r, fmt.Errorf("the upload did not report what it sent")
	}
	return r, nil
}

// applyDeltaCmd writes one incremental's ranges onto target. The stream has to
// hold exactly the bytes the ranges add up to: a short stream would leave the
// tail ranges unwritten while every dd exited 0, so the count is checked after.
func applyDeltaCmd(pullRanges, pullData, target string) string {
	return fmt.Sprintf(`set -e -o pipefail
R=$(mktemp); CNT=$(mktemp); trap 'rm -f "$R" "$CNT"' EXIT
%s > "$R"
WANT=$(awk '{s+=$2} END {printf "%%.0f", s}' "$R")
%s | gzip -dc | tee >(wc -c > "$CNT") | {
  while read -r -u3 off len; do
    sudo dd of=%s bs=1M iflag=fullblock,count_bytes oflag=seek_bytes seek="$off" count="$len" conv=notrunc status=none
  done 3<"$R"
  cat > /dev/null
}
for i in $(seq 1 100); do [ -s "$CNT" ] && break; sleep 0.1; done
GOT=$(cat "$CNT"); [ "$GOT" = "$WANT" ] || { echo "the change stream holds $GOT bytes, the ranges add up to $WANT" >&2; exit 1; }
sudo blockdev --flushbufs %s`, pullRanges, pullData, target, target)
}

// keepBases records which new snapshots stay on the node as the base for the
// next incremental. Only a backup whose every volume is thin keeps any: a thick
// LVM snapshot slows every write to its origin for as long as it exists, and
// half a set of bases cannot serve as one.
func (bm *BackupManager) keepBases(ctx context.Context, host string, rec *database.Backup, snaps map[uint32]string) map[uint32]bool {
	keep := make(map[uint32]bool, len(rec.Volumes))
	for _, v := range rec.Volumes {
		thin, err := bm.controller.deployment.LVIsThin(ctx, host, v.Pool, snaps[v.VolumeID])
		if err != nil || !thin {
			return map[uint32]bool{}
		}
		keep[v.VolumeID] = true
	}
	for i := range rec.Volumes {
		rec.Volumes[i].Snapshot = snaps[rec.Volumes[i].VolumeID]
	}
	return keep
}

// releaseBases removes the base snapshots rec kept and records that they are
// gone. Failures are logged: a leftover snapshot costs pool space, not data.
func (bm *BackupManager) releaseBases(ctx context.Context, rec *database.Backup) {
	host := bm.controller.ResolveHost(rec.Node)
	changed := false
	for i, v := range rec.Volumes {
		if v.Snapshot == "" {
			continue
		}
		if _, err := bm.controller.deployment.LVRemoveSnapshot(ctx, []string{host}, v.Pool, v.Snapshot); err != nil {
			bm.controller.logger.Warn("Failed to remove a backup base snapshot",
				zap.String("backup", rec.ID), zap.String("snapshot", v.Snapshot), zap.Error(err))
			continue
		}
		rec.Volumes[i].Snapshot = ""
		changed = true
	}
	if changed {
		if err := bm.controller.db.SaveBackup(ctx, rec); err != nil {
			bm.controller.logger.Warn("Failed to record a released base snapshot",
				zap.String("backup", rec.ID), zap.Error(err))
		}
	}
}

// releaseOlderBases drops the base snapshots every other backup of rec's
// resource on rec's target still holds: rec's own are the base from now on.
func (bm *BackupManager) releaseOlderBases(ctx context.Context, rec *database.Backup) {
	all, err := bm.controller.db.ListBackups(ctx, rec.Resource, rec.Target)
	if err != nil {
		return
	}
	for _, b := range all {
		if b.ID != rec.ID {
			bm.releaseBases(ctx, b)
		}
	}
}

// readable reports whether node holds an UpToDate replica a backup may be read
// from: never the asynchronous DR copy, which may be behind.
func (bm *BackupManager) readable(info *ResourceInfo, node string) bool {
	if node == "" || (info.WANMode && node == info.DRNode) {
		return false
	}
	st := info.NodeStates[node]
	return st != nil && st.DiskState == "UpToDate"
}

// dependents lists the backups built directly on id.
func (bm *BackupManager) dependents(ctx context.Context, rec *database.Backup) ([]string, error) {
	all, err := bm.controller.db.ListBackups(ctx, rec.Resource, rec.Target)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, b := range all {
		if b.Parent == rec.ID && b.State == database.BackupStateCompleted {
			out = append(out, b.ID)
		}
	}
	return out, nil
}
