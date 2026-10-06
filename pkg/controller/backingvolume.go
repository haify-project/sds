package controller

import (
	"context"
	"fmt"
)

// createBackingVolumeOn makes one replica's backing volume on one node, in
// whichever shape that node's pool actually has.
//
// Three call sites used to decide this three different ways: resource creation
// switched on the pool type recorded in the database and guessed the thin
// pool's name from a "<pool>_thin" convention, while add-replica and add-dr
// ran a plain thick `lvcreate` regardless. On a node whose volume group is
// entirely consumed by a thin pool the thick path cannot succeed at all —
// there is no free space left to take, by construction.
//
// The name is not guessed either. A pool built by `pool create` is called
// "<pool>_thin" and one produced by converting a thick pool in place is called
// something else, so the node is asked what it has rather than told.
func (rm *ResourceManager) createBackingVolumeOn(ctx context.Context, host, pool, volume string, sizeBytes uint64) error {
	thinPool, err := rm.deployment.LVThinPoolIn(ctx, host, pool)
	if err != nil {
		return fmt.Errorf("look for a thin pool in %s on %s: %w", pool, host, err)
	}

	// Bytes, never a rounded size: DRBD stores the device size in its metadata,
	// and a replica built at a different number of bytes will not attach.
	size := fmt.Sprintf("%dB", sizeBytes)

	if thinPool != "" {
		return execFailure(rm.deployment.LVCreateThinVolume(ctx, []string{host}, pool, thinPool, volume, size))
	}
	return execFailure(rm.createThickLV(ctx, []string{host}, pool, volume, size))
}
