package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A pool with a thin pool in it reports the thin pool as what new volumes get,
// and says it is thin, whatever type it was recorded as; the volume group's
// own numbers stay in total_*/free_*.
func TestPoolInfoAllocatable(t *testing.T) {
	thin := pbPoolInfo(&PoolInfo{Name: "haify_p", Type: "vg", TotalBytes: 20 << 30, FreeBytes: 3 << 30,
		ThinUsage: &PoolThinInfo{PoolLV: "haify_p_thin", SizeBytes: 16 << 30, DataPercent: 25}})
	assert.True(t, thin.Thin)
	assert.Equal(t, uint64(16<<30), thin.CapacityBytes)
	assert.Equal(t, uint64(12<<30), thin.AvailableBytes)
	assert.Equal(t, uint64(3<<30), thin.FreeBytes, "the group's own figure is unchanged")

	thick := pbPoolInfo(&PoolInfo{Name: "haify_q", Type: "vg", TotalBytes: 10 << 30, FreeBytes: 4 << 30})
	assert.False(t, thick.Thin)
	assert.Equal(t, uint64(10<<30), thick.CapacityBytes)
	assert.Equal(t, uint64(4<<30), thick.AvailableBytes)

	unmeasured := pbPoolInfo(&PoolInfo{Name: "haify_r", ThinUsage: &PoolThinInfo{PoolLV: "t"}})
	assert.Zero(t, unmeasured.AvailableBytes, "a thin pool of unknown size is not reported as one")
	assert.False(t, unmeasured.Thin)
}
