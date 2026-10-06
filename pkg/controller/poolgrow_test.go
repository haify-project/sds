package controller

import (
	"context"
	"testing"

	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// growRecorder captures the thin pool extension calls AddDiskToPool makes.
type growRecorder struct {
	order        []string
	metaGrowTo   uint64
	extendVG     string
	extendPool   string
	extendPctArg int
}

func thinAddDiskDeployment(rec *growRecorder, thinLV string, dataBytes, metaBytes, freeBytes uint64) *fakeDeploymentClient {
	return &fakeDeploymentClient{
		lvThinPoolInFunc: func(context.Context, string, string) (string, error) { return thinLV, nil },
		lvSizeBytesFunc: func(_, _, lv string) (uint64, error) {
			if lv == thinLV+"_tmeta" {
				return metaBytes, nil
			}
			return dataBytes, nil
		},
		vgFreeBytesFunc: func(string, string) (uint64, error) { return freeBytes, nil },
		lvExtendThinPoolMetadataFunc: func(_ context.Context, hosts []string, _, _ string, size uint64) (*deployment.ExecResult, error) {
			rec.order = append(rec.order, "metadata")
			rec.metaGrowTo = size
			return successExecResult(hosts, ""), nil
		},
		lvExtendThinPoolPercentFreeFunc: func(hosts []string, vg, pool string, pct int) (*deployment.ExecResult, error) {
			rec.order = append(rec.order, "data")
			rec.extendVG, rec.extendPool, rec.extendPctArg = vg, pool, pct
			return successExecResult(hosts, ""), nil
		},
	}
}

// A disk added to a thin-backed pool must end up inside the thin pool: thin
// volumes are carved from the thin pool LV, so space left in the group after
// vgextend is invisible to them.
func TestAddDiskToPoolGrowsThinPool(t *testing.T) {
	rec := &growRecorder{}
	// 95 GiB pool with the 128 MiB metadata floor; a 100 GiB disk arrives.
	dep := thinAddDiskDeployment(rec, "sds_vg0_thin", 95*gib, thinMetadataFloor, 105*gib)

	err := newBasicTestController(dep).storage.AddDiskToPool(context.Background(), "vg0", "/dev/sdc", "n1")
	require.NoError(t, err)

	assert.Equal(t, []string{"metadata", "data"}, rec.order, "metadata grows before data takes the free extents")
	assert.Equal(t, "sds_vg0", rec.extendVG)
	assert.Equal(t, "sds_vg0_thin", rec.extendPool)
	assert.Equal(t, thinPoolGrowPercentFree, rec.extendPctArg, "same share of free extents as pool create")
	assert.Equal(t, thinMetadataBytes(95*gib+105*gib*95/100), rec.metaGrowTo,
		"metadata is sized for the grown pool with the convert-thin rule")
}

func TestAddDiskToPoolLeavesThickPoolAlone(t *testing.T) {
	rec := &growRecorder{}
	dep := thinAddDiskDeployment(rec, "", 0, 0, 100*gib)

	err := newBasicTestController(dep).storage.AddDiskToPool(context.Background(), "vg0", "/dev/sdc", "n1")
	require.NoError(t, err)
	assert.Empty(t, rec.order, "a group without a thin pool already sees the new space")
}

func TestAddDiskToPoolReportsThinGrowFailure(t *testing.T) {
	rec := &growRecorder{}
	dep := thinAddDiskDeployment(rec, "sds_vg0_thin", 95*gib, thinMetadataFloor, 105*gib)
	dep.lvExtendThinPoolPercentFreeFunc = func(hosts []string, _, _ string, _ int) (*deployment.ExecResult, error) {
		return failedResult(hosts, "Insufficient free space"), nil
	}

	err := newBasicTestController(dep).storage.AddDiskToPool(context.Background(), "vg0", "/dev/sdc", "n1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "thin pool was not grown")
	assert.Contains(t, err.Error(), "Insufficient free space")
}

func TestPlanThinGrow(t *testing.T) {
	// Nothing free: nothing to do.
	assert.Equal(t, thinGrowPlan{}, planThinGrow(95*gib, thinMetadataFloor, 0, thinPoolGrowPercentFree))

	// Metadata already large enough for the grown pool is left alone.
	p := planThinGrow(95*gib, 16*gib, 105*gib, thinPoolGrowPercentFree)
	assert.True(t, p.ExtendData)
	assert.Zero(t, p.MetadataGrowTo)

	// A metadata growth that would not fit in the unallocated share is skipped
	// rather than starving the data extension.
	p = planThinGrow(2000*gib, thinMetadataFloor, 1*gib, thinPoolGrowPercentFree)
	assert.True(t, p.ExtendData)
	assert.Zero(t, p.MetadataGrowTo)
}
