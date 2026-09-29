package controller

import (
	"context"
	"sort"

	"go.uber.org/zap"
)

// Retention counts snapshots; it does not look at the pool they fill. On a
// thin pool every snapshot holds the blocks written since it was taken, so a
// busy volume's history can outgrow the pool while staying well inside its
// policy. The pool fills, the next snapshot cannot be created, and retention,
// which is measured from the newest snapshot there is, stops removing
// anything: the pool stays full. The replica is what pays — its writes fail
// and DRBD drops the disk. That is what happened to openclaw's replica on
// node-a after 2026-09-28, with the three other nodes at 79–90% on the same
// road.
//
// So after retention, a pool still past the near-full threshold gives up its
// oldest scheduled snapshots of the volume, one at a time, until it is below
// it. A snapshot is a convenience; the replica is the data.

// snapshotsKeptUnderPressure is how many of a volume's newest scheduled
// snapshots survive even a pool that stays full: removing the last ones would
// not free enough to matter and leaves nothing to restore from.
const snapshotsKeptUnderPressure = 2

// relieveThinPool removes the volume's oldest scheduled snapshots on host
// while its thin pool is past ThinPoolNearFullPercent (data or metadata).
func (sm *ScheduleManager) relieveThinPool(ctx context.Context, host, node string, vol *ResourceVolumeInfo) {
	if isZFSDevice(vol.Device) || sm.controller.storage == nil {
		return
	}
	log := sm.controller.logger
	usage, err := sm.controller.storage.readThinUsage(ctx, host, vol.Pool)
	if err != nil || usage == nil || !overThinThreshold(usage) {
		return
	}
	snaps, err := sm.listScheduledSnaps(ctx, host, vol)
	if err != nil {
		return
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].TS.Before(snaps[j].TS) })
	for len(snaps) > snapshotsKeptUnderPressure && overThinThreshold(usage) {
		oldest := snaps[0]
		if _, err := sm.controller.deployment.LVRemoveSnapshot(ctx, []string{host}, vol.Pool, oldest.Name); err != nil {
			log.Warn("Snapshot schedule: could not remove a snapshot to free a full pool",
				zap.String("node", node), zap.String("snapshot", oldest.Name), zap.Error(err))
			return
		}
		log.Warn("Snapshot schedule: removed the oldest snapshot to free a full thin pool",
			zap.String("node", node), zap.String("pool", vol.Pool), zap.String("snapshot", oldest.Name),
			zap.Float64("data_percent", usage.DataPercent), zap.Float64("metadata_percent", usage.MetaPercent))
		snaps = snaps[1:]
		if usage, err = sm.controller.storage.readThinUsage(ctx, host, vol.Pool); err != nil || usage == nil {
			return
		}
	}
}

func overThinThreshold(u *PoolThinInfo) bool {
	return u.OutOfSpace || u.DataPercent >= ThinPoolNearFullPercent || u.MetaPercent >= ThinPoolNearFullPercent
}
