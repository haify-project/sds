package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DRBD's internal metadata is carved off the END of the backing volume, so a
// volume created at exactly the requested size exports a DRBD device that is
// SMALLER than requested. Writing an image of exactly the nominal size then
// fails ("Cannot grow device files") or silently loses the tail — which is how
// a guest ended up with a truncated GPT backup header and dropped to an
// initramfs shell during real-hardware validation.

func TestDRBDMetadataMatchesMeasuredOverhead(t *testing.T) {
	// Measured on a live 4 GiB, 3-node resource: the DRBD device was 292 KiB
	// smaller than its backing LV (36 KiB fixed + 2 peers x 128 KiB bitmap).
	// The allowance must be at least that, and is rounded up to 1 MiB.
	const fourGiB = 4 * 1024 * 1024 * 1024
	measured := uint64(292 * 1024)

	got := drbdMetadataBytes(fourGiB, 2)
	assert.GreaterOrEqual(t, got, measured,
		"allowance must cover the overhead actually observed on hardware")
	assert.Equal(t, uint64(0), got%(1024*1024), "rounded up to a whole MiB")
}

func TestDRBDMetadataGrowsWithSizeAndPeers(t *testing.T) {
	const gib = 1024 * 1024 * 1024

	small := drbdMetadataBytes(gib, 2)
	large := drbdMetadataBytes(100*gib, 2)
	assert.Greater(t, large, small, "bitmap scales with data size")

	fewPeers := drbdMetadataBytes(100*gib, 2)
	manyPeers := drbdMetadataBytes(100*gib, 16)
	assert.Greater(t, manyPeers, fewPeers, "one bitmap per peer")

	// Bitmap slots are fixed at create-md time while diskless clients attach
	// later, so a caller reporting few peers still gets room to grow.
	assert.Equal(t, drbdMetadataBytes(gib, minMetadataPeers), drbdMetadataBytes(gib, 1),
		"peer count is floored so later attachments still fit")
	assert.Equal(t, drbdMetadataBytes(gib, minMetadataPeers), drbdMetadataBytes(gib, 0))
}

// The whole point: the size handed to lvcreate must exceed the requested size,
// so that after DRBD takes its cut the exported device is still big enough.
func TestBackingVolumeSizeArgExceedsRequest(t *testing.T) {
	for _, gb := range []uint32{1, 3, 4, 10, 100} {
		arg := backingVolumeSizeArg(gb, 2)
		require.True(t, strings.HasSuffix(arg, "B"), "sized in bytes, not whole GB: %s", arg)

		var total uint64
		_, err := fmt.Sscanf(arg, "%dB", &total)
		require.NoError(t, err)

		requested := uint64(gb) * 1024 * 1024 * 1024
		assert.Greater(t, total, requested,
			"%d GB volume must be over-allocated to leave room for DRBD metadata", gb)
		assert.Less(t, total-requested, uint64(64*1024*1024),
			"but the over-allocation should stay small")
	}
}

// createBackingVolume walks nodes one at a time, so a failure part-way leaves
// volumes behind. If the rollback's best-effort lvremove misses a node, every
// later attempt at that name hits "already exists" — which permanently blocked
// `qm importdisk` against a real cluster until the stale LV was removed by hand.
func TestCreateBackingVolumeReusesExistingLargeEnoughVolume(t *testing.T) {
	var lvsQueried bool
	dep := &fakeDeploymentClient{
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, _ string) (*deployment.ExecResult, error) {
			res := successExecResult(hosts, "")
			for _, h := range hosts {
				if h == "10.0.0.1" {
					res.Hosts[h] = &deployment.HostResult{
						Host: h, Success: false,
						Output: `  Logical Volume "res1_data" already exists in volume group "vg0"`,
					}
				}
			}
			return res, nil
		},
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "lvs") {
				lvsQueried = true
				// 8 GiB: comfortably larger than the 3 GiB requested.
				return successExecResult(hosts, "  8589934592\n"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	err := ctrl.resources.createBackingVolume(context.Background(),
		[]string{"10.0.0.1", "10.0.0.2"}, []string{"node1", "node2"}, "lvm", "vg0", "res1_data", 3, false)
	require.NoError(t, err, "a leftover volume big enough to reuse must not block the retry")
	assert.True(t, lvsQueried, "reuse must be justified by checking the actual size")
}

// A stale volume that is too SMALL must not be silently reused — that would
// hand the caller a device shorter than they asked for.
func TestCreateBackingVolumeRefusesTooSmallExistingVolume(t *testing.T) {
	dep := &fakeDeploymentClient{
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, _ string) (*deployment.ExecResult, error) {
			res := successExecResult(hosts, "")
			res.Hosts["10.0.0.1"] = &deployment.HostResult{
				Host: "10.0.0.1", Success: false,
				Output: `  Logical Volume "res1_data" already exists in volume group "vg0"`,
			}
			return res, nil
		},
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "lvs") {
				return successExecResult(hosts, "  1073741824\n"), nil // only 1 GiB
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	err := ctrl.resources.createBackingVolume(context.Background(),
		[]string{"10.0.0.1"}, []string{"node1"}, "lvm", "vg0", "res1_data", 3, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too small to reuse")
}

// Any failure that is NOT a name collision stays a hard error.
func TestCreateBackingVolumeStillFailsOnRealErrors(t *testing.T) {
	dep := &fakeDeploymentClient{
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, _ string) (*deployment.ExecResult, error) {
			res := successExecResult(hosts, "")
			res.Hosts["10.0.0.1"] = &deployment.HostResult{
				Host: "10.0.0.1", Success: false,
				Output: "  Volume group \"vg0\" has insufficient free space",
			}
			return res, nil
		},
	}
	ctrl := newBasicTestController(dep)

	err := ctrl.resources.createBackingVolume(context.Background(),
		[]string{"10.0.0.1"}, []string{"node1"}, "lvm", "vg0", "res1_data", 3, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient free space")
}

// The over-allocation must actually reach lvcreate.
func TestCreateBackingVolumePassesOverAllocatedSize(t *testing.T) {
	var gotSize string
	dep := &fakeDeploymentClient{
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, size string) (*deployment.ExecResult, error) {
			gotSize = size
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	require.NoError(t, ctrl.resources.createBackingVolume(context.Background(),
		[]string{"10.0.0.1"}, []string{"node1"}, "lvm", "vg0", "res1_data", 4, false))

	var total uint64
	_, err := fmt.Sscanf(gotSize, "%dB", &total)
	require.NoError(t, err, "size arg %q should be in bytes", gotSize)
	assert.Greater(t, total, uint64(4)*1024*1024*1024,
		"lvcreate must be asked for more than the nominal 4 GiB")
}
