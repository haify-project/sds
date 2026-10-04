package controller

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// ZFS holds on locked scheduled snapshots.
//
// sds refuses to delete a locked snapshot (snapshot_lock.go), but a ZFS
// snapshot can also be destroyed on the node itself — by a cleanup script, a
// `zfs destroy -r` of the dataset, an operator in a hurry. A hold makes ZFS
// itself refuse ("dataset is busy") until it is released. Root can release
// it, so it is a guard against mistakes and careless tooling, not against an
// attacker with root; the S3 Object Lock backups are that.
//
// The schedule places the hold when it takes a snapshot under a lock, and
// sds releases it right before it deletes the snapshot once the lock has
// passed (retention, or `zfs snapshot delete`).

const zfsLockHoldTag = "sds-lock"

// zfsSnapshotRef is "<dataset>@<name>" checked for what a ZFS name may
// contain, so it can go into a command line unquoted.
func zfsSnapshotRef(snapshot string) (string, error) {
	dataset, name, ok := strings.Cut(snapshot, "@")
	if !ok || dataset == "" || name == "" {
		return "", fmt.Errorf("%q is not a ZFS snapshot", snapshot)
	}
	for _, r := range snapshot {
		if !zfsNameRune(r) {
			return "", fmt.Errorf("%q has a character a ZFS snapshot name cannot", snapshot)
		}
	}
	return snapshot, nil
}

func zfsNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-.:/@", r)
}

// holdLockedZFSSnapshot places the lock hold on a snapshot just taken.
func (c *Controller) holdLockedZFSSnapshot(ctx context.Context, host, snapshot string) {
	ref, err := zfsSnapshotRef(snapshot)
	if err == nil {
		res, e := c.deployment.Exec(ctx, []string{host}, "sudo zfs hold "+zfsLockHoldTag+" "+ref)
		switch {
		case e != nil:
			err = e
		case res != nil && !res.AllSuccess():
			err = fmt.Errorf("%s", res.FailureDetails())
		}
	}
	if err != nil {
		// The snapshot is still locked by sds; only the node-side guard is
		// missing.
		c.logger.Warn("Could not place the ZFS hold on a locked snapshot",
			zap.String("host", host), zap.String("snapshot", snapshot), zap.Error(err))
	}
}

// releaseZFSLockHold lifts the lock hold before sds deletes a snapshot whose
// lock has passed. A snapshot without the hold is fine.
func (c *Controller) releaseZFSLockHold(ctx context.Context, host, snapshot string) {
	ref, err := zfsSnapshotRef(snapshot)
	if err != nil {
		return
	}
	cmd := fmt.Sprintf("if sudo zfs holds -H %s 2>/dev/null | grep -qw %s; then sudo zfs release %s %s; fi",
		ref, zfsLockHoldTag, zfsLockHoldTag, ref)
	if _, err := c.deployment.Exec(ctx, []string{host}, cmd); err != nil {
		c.logger.Warn("Could not release the ZFS lock hold", zap.String("snapshot", snapshot), zap.Error(err))
	}
}
