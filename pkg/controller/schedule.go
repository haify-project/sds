package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
)

// schedSnapMarker separates a backing volume name from the UTC timestamp in a
// scheduled snapshot's name. Snapshots carrying it are managed by the
// scheduler; manual snapshots are left untouched by retention.
const schedSnapMarker = "_sched_"

// snapTimeLayout is the UTC timestamp embedded in scheduled snapshot names.
// It uses only characters valid in both LVM LV names and ZFS snapshot names.
const snapTimeLayout = "20060102T150405Z"

// buildSnapName names a scheduled snapshot of backingVolume taken at ts, e.g.
// "data_data_sched_20240101T020000Z".
func buildSnapName(backingVolume string, ts time.Time) string {
	return backingVolume + schedSnapMarker + ts.UTC().Format(snapTimeLayout)
}

// parseSnapName splits a snapshot name into its backing volume and timestamp.
// ok is false for names that are not scheduler-managed.
func parseSnapName(name string) (backingVolume string, ts time.Time, ok bool) {
	idx := strings.LastIndex(name, schedSnapMarker)
	if idx < 0 {
		return "", time.Time{}, false
	}
	prefix := name[:idx]
	tsStr := name[idx+len(schedSnapMarker):]
	parsed, err := time.ParseInLocation(snapTimeLayout, tsStr, time.UTC)
	if err != nil || prefix == "" {
		return "", time.Time{}, false
	}
	return prefix, parsed, true
}

// scheduledSnap is one snapshot considered by retention.
type scheduledSnap struct {
	Name string
	TS   time.Time
}

// gfsHasRetention reports whether a policy keeps anything at all.
func gfsHasRetention(p database.GFSPolicy) bool {
	return p.Hourly > 0 || p.Daily > 0 || p.Weekly > 0 || p.Monthly > 0 || p.Yearly > 0
}

// selectExpiredSnapshots applies a GFS retention policy and returns the
// snapshots that should be deleted. For each time bucket it keeps the newest
// snapshot in each distinct period, up to that bucket's count; a snapshot kept
// by any bucket survives. An all-zero policy is treated as "keep everything"
// so a misconfiguration can never wipe a resource's snapshots.
func selectExpiredSnapshots(snaps []scheduledSnap, policy database.GFSPolicy) []scheduledSnap {
	if !gfsHasRetention(policy) || len(snaps) == 0 {
		return nil
	}

	sorted := append([]scheduledSnap(nil), snaps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TS.After(sorted[j].TS) })

	kept := make(map[string]bool, len(sorted))
	keepBucket := func(count int, keyFn func(time.Time) string) {
		if count <= 0 {
			return
		}
		seen := make(map[string]bool, count)
		for _, s := range sorted {
			k := keyFn(s.TS.UTC())
			if seen[k] {
				continue
			}
			seen[k] = true
			kept[s.Name] = true
			if len(seen) >= count {
				break
			}
		}
	}

	keepBucket(policy.Hourly, func(t time.Time) string { return t.Format("2006010215") })
	keepBucket(policy.Daily, func(t time.Time) string { return t.Format("20060102") })
	keepBucket(policy.Weekly, func(t time.Time) string {
		y, w := t.ISOWeek()
		return fmt.Sprintf("%04d-%02d", y, w)
	})
	keepBucket(policy.Monthly, func(t time.Time) string { return t.Format("200601") })
	keepBucket(policy.Yearly, func(t time.Time) string { return t.Format("2006") })

	var expired []scheduledSnap
	for _, s := range sorted {
		if !kept[s.Name] {
			expired = append(expired, s)
		}
	}
	return expired
}

// ScheduleManager runs cron-driven snapshot and backup schedules on the active
// controller and prunes old snapshots and backups per each schedule's GFS
// retention policy.
type ScheduleManager struct {
	controller *Controller
	mu         sync.Mutex
	cron       *cron.Cron
	started    bool
	// verifying is set while a verify sweep runs, so a slow one is not
	// joined by the next.
	verifying atomic.Bool
}

// NewScheduleManager creates a snapshot schedule manager.
func NewScheduleManager(c *Controller) *ScheduleManager {
	return &ScheduleManager{controller: c}
}

// Start loads persisted schedules and begins firing them. Safe to call once on
// the active controller; a no-op if already started.
func (sm *ScheduleManager) Start(ctx context.Context) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.started {
		return nil
	}
	if err := sm.rebuildLocked(ctx); err != nil {
		return err
	}
	sm.cron.Start()
	sm.started = true
	sm.controller.logger.Info("Snapshot scheduler started")
	return nil
}

// Stop halts all scheduled snapshots. Safe to call when not started.
func (sm *ScheduleManager) Stop() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !sm.started {
		return
	}
	if sm.cron != nil {
		sm.cron.Stop()
	}
	sm.started = false
	sm.controller.logger.Info("Snapshot scheduler stopped")
}

// CreateSchedule validates and persists a snapshot schedule, then reloads the
// cron so it takes effect immediately. One schedule per resource: the schedule
// name is the resource name.
//
// lockDays, when set, locks the snapshots the schedule takes (snapshot_lock.go);
// nil keeps the lock of the schedule being replaced.
func (sm *ScheduleManager) CreateSchedule(ctx context.Context, resource, cronExpr string, policy database.GFSPolicy, enabled bool, lockDays *int) error {
	if err := validateCron(cronExpr); err != nil {
		return err
	}
	if !gfsHasRetention(policy) {
		return fmt.Errorf("retention policy keeps nothing: set at least one of hourly/daily/weekly/monthly/yearly")
	}
	if sm.controller.db == nil {
		return fmt.Errorf("database not available")
	}
	if _, err := sm.controller.db.GetResource(ctx, resource); err != nil {
		return fmt.Errorf("resource %q not found", resource)
	}
	old, _ := sm.controller.db.GetSnapshotSchedule(ctx, resource)
	lock := 0
	if old != nil {
		lock = old.LockDays
	}
	if lockDays != nil {
		lock = *lockDays
	}
	if err := checkScheduleLockChange(old, lock, lockNow()); err != nil {
		return err
	}
	s := &database.SnapshotSchedule{
		Name:     resource,
		Resource: resource,
		Cron:     cronExpr,
		Enabled:  enabled,
		Keep:     policy,
		LockDays: lock,
	}
	if old != nil {
		// The last run is what says how long the newest snapshot stays
		// locked, and a freeze outlives a replaced schedule too.
		s.LastRun, s.CreatedAt = old.LastRun, old.CreatedAt
		s.FrozenUntil, s.FrozenAt, s.FrozenReason = old.FrozenUntil, old.FrozenAt, old.FrozenReason
	}
	if err := sm.controller.db.SaveSnapshotSchedule(ctx, s); err != nil {
		return fmt.Errorf("save schedule: %w", err)
	}
	sm.controller.logger.Info("Created snapshot schedule",
		zap.String("resource", resource), zap.String("cron", cronExpr), zap.Bool("enabled", enabled))
	return sm.reload(ctx)
}

// DeleteSchedule removes a schedule. Existing snapshots are left in place;
// only future scheduled snapshots and pruning stop.
func (sm *ScheduleManager) DeleteSchedule(ctx context.Context, name string) error {
	if sm.controller.db == nil {
		return fmt.Errorf("database not available")
	}
	if s, err := sm.controller.db.GetSnapshotSchedule(ctx, name); err == nil && s != nil {
		if until := scheduleLockedUntil(s, lockNow()); !until.IsZero() {
			return fmt.Errorf("the schedule of %s locks its snapshots and the newest stay locked until %s; it can be deleted after that",
				s.Resource, until.UTC().Format(time.RFC3339))
		}
	}
	if err := sm.controller.db.DeleteSnapshotSchedule(ctx, name); err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	sm.controller.logger.Info("Deleted snapshot schedule", zap.String("name", name))
	return sm.reload(ctx)
}

// ListSchedules returns all schedules.
func (sm *ScheduleManager) ListSchedules(ctx context.Context) ([]*database.SnapshotSchedule, error) {
	if sm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	return sm.controller.db.ListSnapshotSchedules(ctx)
}

// NextRun returns the next fire time of a cron expression after now, or the
// zero time if the expression is invalid.
func NextRun(cronExpr string, now time.Time) time.Time {
	sched, err := cron.ParseStandard(cronExpr)
	if err != nil {
		return time.Time{}
	}
	return sched.Next(now)
}

// validateCron checks a standard 5-field cron expression.
func validateCron(cronExpr string) error {
	if _, err := cron.ParseStandard(cronExpr); err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}
	return nil
}

// reload rebuilds cron entries from the database. Called after any schedule
// change so edits take effect without a controller restart.
func (sm *ScheduleManager) reload(ctx context.Context) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !sm.started {
		return nil
	}
	old := sm.cron
	if err := sm.rebuildLocked(ctx); err != nil {
		return err
	}
	sm.cron.Start()
	if old != nil {
		old.Stop()
	}
	return nil
}

// rebuildLocked constructs a fresh (stopped) cron populated with every enabled
// schedule. The caller holds sm.mu and starts the cron.
func (sm *ScheduleManager) rebuildLocked(ctx context.Context) error {
	c := cron.New()
	schedules, err := sm.controller.db.ListSnapshotSchedules(ctx)
	if err != nil {
		return fmt.Errorf("load snapshot schedules: %w", err)
	}
	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		name := s.Name
		if _, err := c.AddFunc(s.Cron, func() { sm.runSchedule(name) }); err != nil {
			sm.controller.logger.Warn("Skipping snapshot schedule with invalid cron",
				zap.String("schedule", name),
				zap.String("cron", s.Cron),
				zap.Error(err))
		}
	}
	if err := sm.addBackupSchedules(ctx, c); err != nil {
		return err
	}
	if spec := sm.verifySchedule(); spec != "" {
		if _, err := c.AddFunc(spec, sm.runVerifySweep); err != nil {
			sm.controller.logger.Warn("Skipping the verify schedule: invalid cron",
				zap.String("cron", spec), zap.Error(err))
		}
	}
	if spec := sm.inspectSchedule(); spec != "" {
		if _, err := c.AddFunc(spec, sm.runInspectionTick); err != nil {
			sm.controller.logger.Warn("Skipping the inspection schedule: invalid cron",
				zap.String("cron", spec), zap.Error(err))
		}
	}
	sm.cron = c
	return nil
}

func (sm *ScheduleManager) verifySchedule() string {
	if sm.controller.config == nil {
		return ""
	}
	return strings.TrimSpace(sm.controller.config.Storage.VerifySchedule)
}

// runSchedule executes one schedule: snapshot the resource's volumes on every
// diskful node, then prune per the GFS policy. Per-node failures are logged
// and do not abort the rest.
func (sm *ScheduleManager) runSchedule(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	log := sm.controller.logger

	s, err := sm.controller.db.GetSnapshotSchedule(ctx, name)
	if err != nil || s == nil || !s.Enabled {
		return
	}

	res, err := sm.controller.resources.GetResource(ctx, s.Resource)
	if err != nil {
		log.Warn("Snapshot schedule: resource lookup failed",
			zap.String("schedule", name), zap.String("resource", s.Resource), zap.Error(err))
		return
	}
	if len(res.Volumes) == 0 || len(res.Nodes) == 0 {
		log.Warn("Snapshot schedule: resource has no diskful volumes/nodes",
			zap.String("schedule", name), zap.String("resource", s.Resource))
		return
	}

	ts := time.Now().UTC()
	log.Info("Running snapshot schedule",
		zap.String("schedule", name), zap.String("resource", s.Resource),
		zap.Int("nodes", len(res.Nodes)), zap.Int("volumes", len(res.Volumes)))

	lock := time.Duration(s.LockDays) * 24 * time.Hour
	frozen := !scheduleFrozenUntil(s, lockNow()).IsZero()
	sm.snapshotResource(ctx, res, s, ts)
	for _, node := range res.Nodes {
		host := sm.controller.ResolveHost(node)
		for _, vol := range res.Volumes {
			// A frozen schedule removes nothing, not even to relieve a full
			// pool: what it froze is the history from before something
			// started rewriting the volume.
			if frozen {
				continue
			}
			sm.pruneVolume(ctx, host, node, vol, s.Keep, lock)
			sm.relieveThinPool(ctx, host, node, s.Resource, vol, lock)
		}
	}

	s.LastRun = time.Now()
	if err := sm.controller.db.SaveSnapshotSchedule(ctx, s); err != nil {
		log.Warn("Snapshot schedule: failed to record last run",
			zap.String("schedule", name), zap.Error(err))
	}
}

// snapshotVolume takes one storage-native snapshot of vol on host.
func (sm *ScheduleManager) snapshotVolume(ctx context.Context, host, node string, vol *ResourceVolumeInfo, ts time.Time) {
	log := sm.controller.logger
	snapName := buildSnapName(vol.BackingVolume, ts)
	dep := sm.controller.deployment

	if isZFSDevice(vol.Device) {
		dataset := fmt.Sprintf("%s/%s", vol.Pool, vol.BackingVolume)
		res, err := dep.ZFSSnapshot(ctx, []string{host}, dataset, snapName)
		logSnapResult(log, "zfs", node, snapName, res, err)
		return
	}

	// LVM: thin snapshots size from the pool; thick snapshots need a COW size.
	thin, err := dep.LVIsThin(ctx, host, vol.Pool, vol.BackingVolume)
	if err != nil {
		log.Warn("Snapshot schedule: LVIsThin failed; assuming thick",
			zap.String("node", node), zap.String("volume", vol.BackingVolume), zap.Error(err))
	}
	if thin {
		res, e := dep.LVCreateThinSnapshot(ctx, []string{host}, vol.Pool, vol.BackingVolume, snapName)
		logSnapResult(log, "lvm-thin", node, snapName, res, e)
		return
	}
	// Thick LVM snapshots need an absolute COW size (-L); reserve ~20% of the
	// origin, with a floor so tiny volumes still get usable headroom.
	res, e := dep.LVCreateSnapshot(ctx, []string{host}, vol.Pool, vol.BackingVolume, snapName, cowSize(vol.SizeGB))
	logSnapResult(log, "lvm", node, snapName, res, e)
}

// cowSize returns a copy-on-write reservation (~20% of an originGB-sized
// volume) as an lvcreate -L argument, never below 256 MiB.
func cowSize(originGB uint64) string {
	return fmt.Sprintf("%dM", cowBytes(originGB)>>20)
}

// pruneVolume lists scheduled snapshots of vol on host and deletes those the
// GFS policy no longer retains.
//
// A snapshot still inside its lock window is kept whatever the policy says; a
// later run prunes it once the lock has expired.
func (sm *ScheduleManager) pruneVolume(ctx context.Context, host, node string, vol *ResourceVolumeInfo, policy database.GFSPolicy, lock time.Duration) {
	log := sm.controller.logger
	snaps, err := sm.listScheduledSnaps(ctx, host, vol)
	if err != nil {
		log.Warn("Snapshot schedule: list snapshots failed",
			zap.String("node", node), zap.String("volume", vol.BackingVolume), zap.Error(err))
		return
	}
	expired := unlockedSnaps(selectExpiredSnapshots(snaps, policy), lock, lockNow())
	dep := sm.controller.deployment
	for _, s := range expired {
		var derr error
		if isZFSDevice(vol.Device) {
			snap := fmt.Sprintf("%s/%s@%s", vol.Pool, vol.BackingVolume, s.Name)
			sm.controller.releaseZFSLockHold(ctx, host, snap)
			_, derr = dep.ZFSDestroySnapshot(ctx, []string{host}, snap)
		} else {
			_, derr = dep.LVRemoveSnapshot(ctx, []string{host}, vol.Pool, s.Name)
		}
		if derr != nil {
			log.Warn("Snapshot schedule: prune failed",
				zap.String("node", node), zap.String("snapshot", s.Name), zap.Error(derr))
			continue
		}
		log.Info("Snapshot schedule: pruned expired snapshot",
			zap.String("node", node), zap.String("snapshot", s.Name))
	}
}

// listScheduledSnaps returns this volume's scheduler-managed snapshots on host.
func (sm *ScheduleManager) listScheduledSnaps(ctx context.Context, host string, vol *ResourceVolumeInfo) ([]scheduledSnap, error) {
	dep := sm.controller.deployment
	var names []string

	if isZFSDevice(vol.Device) {
		dataset := fmt.Sprintf("%s/%s", vol.Pool, vol.BackingVolume)
		res, err := dep.ZFSListSnapshots(ctx, []string{host}, dataset)
		if err != nil {
			return nil, err
		}
		for _, line := range execLines(res, host) {
			// "pool/vol@snapname  used  refer  creation"
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if at := strings.Index(fields[0], "@"); at >= 0 {
				names = append(names, fields[0][at+1:])
			}
		}
	} else {
		res, err := dep.LVListSnapshots(ctx, []string{host}, vol.Pool)
		if err != nil {
			return nil, err
		}
		for _, line := range execLines(res, host) {
			// Pipe-separated, because lv_time carries spaces. Splitting on
			// whitespace worked only as long as the name was first and nothing
			// downstream looked at the other columns.
			name := strings.TrimSpace(line)
			if idx := strings.IndexByte(name, '|'); idx >= 0 {
				name = strings.TrimSpace(name[:idx])
			}
			if name == "" {
				continue
			}
			names = append(names, name)
		}
	}

	var out []scheduledSnap
	for _, n := range names {
		backing, ts, ok := parseSnapName(n)
		if !ok || backing != vol.BackingVolume {
			continue
		}
		out = append(out, scheduledSnap{Name: n, TS: ts})
	}
	return out, nil
}

// isZFSDevice reports whether a volume's device path is a ZFS zvol.
func isZFSDevice(device string) bool {
	return strings.Contains(device, "/dev/zvol/")
}

// execLines returns the trimmed non-empty output lines for host from a result.
func execLines(res *deployment.ExecResult, host string) []string {
	if res == nil {
		return nil
	}
	var lines []string
	for h, hr := range res.Hosts {
		if h != host {
			continue
		}
		for _, raw := range strings.Split(hr.Output, "\n") {
			if t := strings.TrimSpace(raw); t != "" {
				lines = append(lines, t)
			}
		}
	}
	return lines
}

// logSnapResult records the outcome of a single snapshot attempt.
func logSnapResult(log *zap.Logger, kind, node, snapName string, res *deployment.ExecResult, err error) {
	if err != nil {
		log.Warn("Snapshot schedule: snapshot failed",
			zap.String("kind", kind), zap.String("node", node),
			zap.String("snapshot", snapName), zap.Error(err))
		return
	}
	if res != nil && !res.AllSuccess() {
		log.Warn("Snapshot schedule: snapshot failed on host",
			zap.String("kind", kind), zap.String("node", node),
			zap.String("snapshot", snapName), zap.Strings("failed", res.FailedHosts()))
		return
	}
	log.Info("Snapshot schedule: created snapshot",
		zap.String("kind", kind), zap.String("node", node), zap.String("snapshot", snapName))
}
