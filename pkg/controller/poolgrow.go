package controller

import (
	"context"
	"fmt"
)

// Growing a thin pool into space that `pool add-disk` just added to its volume
// group. vgextend alone leaves the new extents unallocated in the group, where
// thin volumes cannot use them: they are carved from the thin pool LV, not
// from the group. Without this step adding a disk to a thin pool changes
// nothing a client can write to.

// thinPoolGrowPercentFree is the share of the group's free extents the thin
// pool's data area is extended by.
//
// It mirrors the 95%FREE `pool create` builds the pool with, for the same
// reason: the remainder stays unallocated so the metadata area (and the spare
// copy LVM keeps of it) can still be grown, and so an operator has room to
// act when the pool fills. Convert-thin takes 100%FREE instead because it
// sizes the metadata area explicitly up front; here the metadata grows after
// the fact and needs extents to grow into.
const thinPoolGrowPercentFree = 95

// thinGrowPlan is what to do to an existing thin pool after its group grew.
type thinGrowPlan struct {
	// MetadataGrowTo is the size to raise the metadata area to, or zero to
	// leave it alone.
	MetadataGrowTo uint64
	// ExtendData is false when the group has nothing free to extend into.
	ExtendData bool
}

// planThinGrow keeps the metadata area proportional to the data area, using
// the same sizing rule convert-thin applies (thinMetadataBytes: 1%, floored
// and capped). Metadata is only ever grown, and only when the growth — paid
// twice, once for the spare copy — fits comfortably in what is free, so a
// short group never has its data extension starved by the metadata one.
func planThinGrow(dataBytes, metaBytes, vgFreeBytes uint64) thinGrowPlan {
	if vgFreeBytes == 0 {
		return thinGrowPlan{}
	}
	plan := thinGrowPlan{ExtendData: true}
	projected := dataBytes + vgFreeBytes*thinPoolGrowPercentFree/100
	if target := thinMetadataBytes(projected); target > metaBytes {
		if 2*(target-metaBytes) < vgFreeBytes*(100-thinPoolGrowPercentFree)/100 {
			plan.MetadataGrowTo = target
		}
	}
	return plan
}

// growThinPoolAfterExtend extends the thin pool in vgName, if it has one, into
// the group's free extents. A group without a thin pool is left alone: thick
// volumes allocate from the group directly and already see the new space.
func (sm *StorageManager) growThinPoolAfterExtend(ctx context.Context, address, vgName string) error {
	dep := sm.controller.deployment
	thinLV, err := dep.LVThinPoolIn(ctx, address, vgName)
	if err != nil {
		return fmt.Errorf("look for a thin pool in %s: %w", vgName, err)
	}
	if thinLV == "" {
		return nil
	}
	dataBytes, err := dep.LVSizeBytes(ctx, address, vgName, thinLV)
	if err != nil {
		return fmt.Errorf("read the size of %s/%s: %w", vgName, thinLV, err)
	}
	metaBytes, err := dep.LVSizeBytes(ctx, address, vgName, thinLV+"_tmeta")
	if err != nil {
		return fmt.Errorf("read the metadata size of %s/%s: %w", vgName, thinLV, err)
	}
	freeBytes, err := dep.VGFreeBytes(ctx, address, vgName)
	if err != nil {
		return fmt.Errorf("read the free space of %s: %w", vgName, err)
	}

	plan := planThinGrow(dataBytes, metaBytes, freeBytes)
	// Metadata first — see LVExtendThinPoolMetadata for why the order matters.
	if plan.MetadataGrowTo > 0 {
		if err := execFailure(dep.LVExtendThinPoolMetadata(ctx, []string{address},
			vgName, thinLV, plan.MetadataGrowTo)); err != nil {
			return fmt.Errorf("grow the metadata area of %s/%s: %w", vgName, thinLV, err)
		}
	}
	if plan.ExtendData {
		if err := execFailure(dep.LVExtendThinPoolPercentFree(ctx, []string{address},
			vgName, thinLV, thinPoolGrowPercentFree)); err != nil {
			return fmt.Errorf("extend %s/%s: %w", vgName, thinLV, err)
		}
	}
	return nil
}
