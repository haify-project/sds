package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/haify-project/haify/pkg/database"
)

// Locked scheduled snapshots.
//
// Ransomware that encrypts a volume rewrites every block, so a thin pool's
// snapshots suddenly hold the whole volume's worth of old data and the pool
// fills. Retention then had the worst possible answer: relieveThinPool deleted
// the OLDEST scheduled snapshots first — the clean ones from before the attack
// — and kept the newest two, taken after it. Anyone with an haify token could
// also simply delete the snapshots, the schedule or the resource.
//
// A schedule's lock_days locks the snapshots it takes: until a snapshot is
// lock_days old, nothing haify does deletes it — not retention, not a full
// pool, not a person through the API — and the schedule and resource cannot
// be deleted, nor the lock shortened, while any of its snapshots is locked.
// The lock is measured from the time in the snapshot's own name, which haify
// wrote when it took it.
//
// What it is not: a lock against root on a storage node, who can lvremove
// anything. That needs the backups on a target with S3 Object Lock
// (backup_lock.go). And it trades one risk for another: a pool that fills
// with locked snapshots is not relieved, its replica's writes fail and DRBD
// drops it. relieveThinPool raises that as a critical event so the pool is
// grown before then.

// maxSnapshotLockDays bounds lock_days: a typo of 3650 for 30 would lock a
// pool for a decade with no way back short of root on the nodes.
const maxSnapshotLockDays = 365

// snapshotLockWindow is how long resource's schedule locks its snapshots.
func (c *Controller) snapshotLockWindow(ctx context.Context, resource string) time.Duration {
	if c.db == nil {
		return 0
	}
	s, err := c.db.GetSnapshotSchedule(ctx, resource)
	if err != nil || s == nil || s.LockDays <= 0 {
		return 0
	}
	return time.Duration(s.LockDays) * 24 * time.Hour
}

// snapshotLockedUntil says until when a snapshot named name, taken by a
// schedule that locks for window, is locked; zero when it is not locked.
func snapshotLockedUntil(name string, window time.Duration, now time.Time) time.Time {
	if window <= 0 {
		return time.Time{}
	}
	_, ts, ok := parseSnapName(name)
	if !ok {
		return time.Time{}
	}
	if until := ts.Add(window); until.After(now) {
		return until
	}
	return time.Time{}
}

// resourceOfBackingVolume names the resource a backing volume belongs to.
func (c *Controller) resourceOfBackingVolume(ctx context.Context, backing string) string {
	if c.db == nil {
		return ""
	}
	resources, err := c.db.ListResources(ctx)
	if err != nil {
		return ""
	}
	for _, r := range resources {
		vols, err := c.db.ListVolumes(ctx, r.Name)
		if err != nil {
			continue
		}
		for _, v := range vols {
			if v.VolumeName == backing {
				return r.Name
			}
		}
	}
	return ""
}

// assertSnapshotUnlocked refuses to delete or consume a locked scheduled
// snapshot. Snapshots haify did not schedule carry no lock.
func (c *Controller) assertSnapshotUnlocked(ctx context.Context, name string) error {
	backing, _, ok := parseSnapName(name)
	if !ok {
		return nil
	}
	resource := c.resourceOfBackingVolume(ctx, backing)
	if resource == "" {
		return nil
	}
	if until := snapshotLockedUntil(name, c.snapshotLockWindow(ctx, resource), lockNow()); !until.IsZero() {
		return fmt.Errorf("snapshot %s is locked by the snapshot schedule of %s until %s and cannot be deleted before then",
			name, resource, until.UTC().Format(time.RFC3339))
	}
	if until, reason := c.resourceFrozenUntil(ctx, resource); !until.IsZero() {
		return fmt.Errorf("the snapshot schedule of %s is frozen until %s (%s): none of its snapshots can be deleted before then",
			resource, until.UTC().Format(time.RFC3339), reason)
	}
	return nil
}

// resourceSnapshotsLockedUntil is the time until which resource may still hold
// locked snapshots: its schedule's last run plus the lock. Zero when none can
// be locked any more.
func (c *Controller) resourceSnapshotsLockedUntil(ctx context.Context, resource string) time.Time {
	if c.db == nil {
		return time.Time{}
	}
	s, err := c.db.GetSnapshotSchedule(ctx, resource)
	if err != nil || s == nil {
		return time.Time{}
	}
	return scheduleLockedUntil(s, lockNow())
}

// scheduleLockedUntil is when the newest snapshot s can have taken stops being
// locked — by its lock, or by a freeze — or zero when that has passed.
func scheduleLockedUntil(s *database.SnapshotSchedule, now time.Time) time.Time {
	until := scheduleFrozenUntil(s, now)
	if s.LockDays <= 0 || s.LastRun.IsZero() {
		return until
	}
	if l := s.LastRun.Add(time.Duration(s.LockDays) * 24 * time.Hour); l.After(now) && l.After(until) {
		return l
	}
	return until
}

// assertResourceUnlocked refuses an operation that would take a resource's
// locked snapshots with it: deleting the resource or one of its volumes.
func (c *Controller) assertResourceUnlocked(ctx context.Context, resource, what string) error {
	if until := c.resourceSnapshotsLockedUntil(ctx, resource); !until.IsZero() {
		return fmt.Errorf("%s would delete snapshots of %s that its snapshot schedule locks until %s; that is refused until then",
			what, resource, until.UTC().Format(time.RFC3339))
	}
	return nil
}

// checkScheduleLockChange validates a schedule's new lock against the one it
// replaces: it may be raised at any time, but not lowered or removed while
// snapshots are locked — that would unlock them.
func checkScheduleLockChange(old *database.SnapshotSchedule, lockDays int, now time.Time) error {
	if lockDays < 0 || lockDays > maxSnapshotLockDays {
		return fmt.Errorf("lock days must be between 0 and %d", maxSnapshotLockDays)
	}
	if old == nil || lockDays >= old.LockDays {
		return nil
	}
	if until := scheduleLockedUntil(old, now); !until.IsZero() {
		return fmt.Errorf("the schedule of %s locks its snapshots for %d days and the newest stay locked until %s; "+
			"the lock can be raised but not lowered before then", old.Resource, old.LockDays, until.UTC().Format(time.RFC3339))
	}
	return nil
}

// unlockedSnaps drops the snapshots that are still locked.
func unlockedSnaps(snaps []scheduledSnap, window time.Duration, now time.Time) []scheduledSnap {
	if window <= 0 {
		return snaps
	}
	out := snaps[:0:0]
	for _, s := range snaps {
		if snapshotLockedUntil(s.Name, window, now).IsZero() {
			out = append(out, s)
		}
	}
	return out
}

// lockPreservingMerge returns the merge to run for restoring snapshotName
// into backing. For a snapshot that is not locked that is the plain merge.
// A locked thin snapshot is merged and then taken again from the restored
// volume under the same name: the volume is exactly the snapshot's content at
// that moment (the resource is down), and the name carries the time the lock
// is measured from, so the lock is unchanged. A locked thick snapshot is
// refused: re-taking it would need a copy-on-write reservation sized for a
// volume about to diverge, and failing that would lose it.
func (sm *SnapshotManager) lockPreservingMerge(ctx context.Context, address, vg, lv, snapshotName, snapshotPath, backing string) (func() error, error) {
	plain := func() error { return sm.mergeSnapshot(ctx, address, snapshotPath, backing) }
	if err := sm.controller.assertSnapshotUnlocked(ctx, snapshotName); err == nil {
		return plain, nil
	}
	thin, err := sm.controller.deployment.LVIsThin(ctx, address, vg, snapshotName)
	if err != nil || !thin {
		return nil, fmt.Errorf("snapshot %s is locked, and restoring merges it away; only a locked thin snapshot can be kept "+
			"through a restore", snapshotName)
	}
	return func() error {
		if err := plain(); err != nil {
			return err
		}
		res, err := sm.controller.deployment.LVCreateThinSnapshot(ctx, []string{address}, vg, lv, snapshotName)
		if err == nil && res != nil && !res.AllSuccess() {
			err = fmt.Errorf("%s", res.FailureDetails())
		}
		if err != nil {
			return fmt.Errorf("restored, but the locked snapshot %s could not be taken again: %w", snapshotName, err)
		}
		return nil
	}, nil
}

// assertRollbackKeepsLocks refuses a ZFS rollback to snapshotName when it
// would destroy a locked snapshot: `zfs rollback -r` destroys every snapshot
// taken after the one rolled back to.
func (sm *SnapshotManager) assertRollbackKeepsLocks(ctx context.Context, address, dataset, snapshotName string) error {
	backing := dataset[strings.LastIndex(dataset, "/")+1:]
	resource := sm.controller.resourceOfBackingVolume(ctx, backing)
	window := sm.controller.snapshotLockWindow(ctx, resource)
	if resource == "" || window <= 0 {
		return nil
	}
	res, err := sm.controller.deployment.Exec(ctx, []string{address},
		fmt.Sprintf("sudo zfs list -t snapshot -H -o name -s creation -d 1 %s", dataset))
	if err != nil || res == nil || !res.AllSuccess() {
		return fmt.Errorf("could not list the snapshots a rollback of %s would destroy; refusing in case one is locked", dataset)
	}
	after, now := false, lockNow()
	var later []string
	for _, line := range execLines(res, address) {
		_, name, ok := strings.Cut(strings.TrimSpace(line), "@")
		if !ok {
			continue
		}
		if after {
			if until := snapshotLockedUntil(name, window, now); !until.IsZero() {
				return fmt.Errorf("rolling %s back to %s destroys every later snapshot, and %s is locked until %s",
					dataset, snapshotName, name, until.UTC().Format(time.RFC3339))
			}
			later = append(later, name)
		}
		if name == snapshotName {
			after = true
		}
	}
	// Their locks have passed, but a hold placed under one would still make
	// the rollback fail.
	for _, name := range later {
		sm.controller.releaseZFSLockHold(ctx, address, dataset+"@"+name)
	}
	return nil
}
