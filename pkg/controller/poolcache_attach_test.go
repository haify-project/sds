package controller

import (
	"context"
	"testing"

	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAttachFake(reports ...string) *fakeDeploymentClient {
	return &fakeDeploymentClient{
		lvThinPoolInFunc: func(context.Context, string, string) (string, error) {
			return "sds_sdspool_thin", nil
		},
		probeBlockDeviceFunc: func(_ context.Context, host, _ string) (*deployment.ExecResult, error) {
			return freeDeviceProbe([]string{host}), nil
		},
		lvsCacheReportFunc: reportSequence(reports...),
	}
}

func TestAttachesAWritethroughCacheByDefault(t *testing.T) {
	dep := newAttachFake("", cachedThinPoolReport("writethrough", "Cwi-aoC---"))
	var mode, cacheVol, lv string
	dep.lvConvertToCacheFunc = func(_ context.Context, hosts []string, vg, l, cv, m string) (*deployment.ExecResult, error) {
		lv, cacheVol, mode = l, cv, m
		assert.Equal(t, "sds_sdspool", vg)
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	info, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.NoError(t, err)
	assert.Equal(t, "writethrough", mode, "an unstated mode must never become writeback")
	assert.Equal(t, cacheVolName, cacheVol)
	// The cache goes in front of the pool, not in front of one volume.
	assert.Equal(t, "sds_sdspool_thin", lv)
	assert.Equal(t, "writethrough", info.Mode)
	assert.Equal(t, "/dev/nvme0n1", info.Device)
}

func TestAttachesWritebackOnlyWhenAskedForByName(t *testing.T) {
	dep := newAttachFake("", cachedThinPoolReport("writeback", "Cwi-aoC---"))
	var mode string
	dep.lvConvertToCacheFunc = func(_ context.Context, hosts []string, _, _, _, m string) (*deployment.ExecResult, error) {
		mode = m
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	info, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "writeback")
	require.NoError(t, err)
	assert.Equal(t, "writeback", mode)
	assert.Equal(t, "writeback", info.Mode)
}

func TestRefusesAnUnknownCacheMode(t *testing.T) {
	ctrl := cacheTestController(t, newAttachFake(""))
	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "wb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown cache mode")
}

// A thick pool has no single LV that every volume passes through, so there is
// nothing a per-pool cache could be attached to.
func TestRefusesToCacheAThickPool(t *testing.T) {
	dep := newAttachFake("")
	dep.lvThinPoolInFunc = func(context.Context, string, string) (string, error) { return "", nil }
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "convert-thin")
}

// ZFS caches through L2ARC and a separate log device; dm-cache under a vdev
// would hide the device from both layers.
func TestRefusesToCacheAZFSPool(t *testing.T) {
	ctrl := cacheTestController(t, newAttachFake(""))
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SavePool(ctx, &database.Pool{Name: "sds_tank", Type: "zfs", Node: "node-a"}))

	_, err := ctrl.storage.AddPoolCache(ctx, "node-a", "tank", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "L2ARC")
}

// Reconfiguring a pool that already has a cache would mean detaching the old
// one — with whatever it still holds — as a side effect of an "add".
func TestRefusesToCacheAPoolThatAlreadyHasOne(t *testing.T) {
	dep := newAttachFake(cachedThinPoolReport("writeback", "Cwi-aoC---"))
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already cached")
}

func TestRefusesADeviceTheNodeSaysIsInUse(t *testing.T) {
	dep := newAttachFake("")
	dep.probeBlockDeviceFunc = func(_ context.Context, host, _ string) (*deployment.ExecResult, error) {
		return successExecResult([]string{host},
			"block=yes\nsize=549755813888\nfstype=LVM2_member\nmount=\nholders=\npvvg=vg_root"), nil
	}
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vg_root")
}

// lvconvert exits non-zero through the result, never through the returned
// error; a failed attach must not leave the cache volume behind, because the
// obvious retry would then fail on the name rather than on the real problem.
func TestUndoesAPartialAttach(t *testing.T) {
	dep := newAttachFake("")
	var removed, released string
	dep.lvConvertToCacheFunc = func(_ context.Context, hosts []string, _, _, _, _ string) (*deployment.ExecResult, error) {
		return failedResult(hosts, "  Insufficient free space: 8 extents needed, but only 0 available"), nil
	}
	dep.lvRemoveFunc = func(_ context.Context, hosts []string, path string) (*deployment.ExecResult, error) {
		removed = path
		return successExecResult(hosts, ""), nil
	}
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, device string) (*deployment.ExecResult, error) {
		released = device
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Insufficient free space")
	assert.Equal(t, "sds_sdspool/"+cacheVolName, removed)
	assert.Equal(t, "/dev/nvme0n1", released)
}

func TestReportsAnAttachThatLeftNoCacheBehind(t *testing.T) {
	// lvconvert exited zero, but the pool still has no cache.
	ctrl := cacheTestController(t, newAttachFake("", ""))
	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no cache")
}

// The mode is read back rather than reported from the request: an lvconvert
// that succeeded while applying a different mode would leave the operator
// believing writes are durable when they are not.
func TestReportsACacheAttachedInTheWrongMode(t *testing.T) {
	ctrl := cacheTestController(t, newAttachFake("", cachedThinPoolReport("writeback", "Cwi-aoC---")))
	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writeback")
	assert.Contains(t, err.Error(), "writethrough")
}

// A pool whose current cache state cannot be read is a pool this cannot reason
// about, and attaching a second cache to one that already has one is not a
// recoverable mistake.
func TestRefusesToAttachWhenTheCacheStateCannotBeRead(t *testing.T) {
	dep := newAttachFake("")
	dep.lvsCacheReportFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return failedResult(hosts, "  Volume group sds_sdspool not found"), nil
	}
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRefusesToAttachWhenTheDeviceCannotBeProbed(t *testing.T) {
	dep := newAttachFake("")
	dep.probeBlockDeviceFunc = func(_ context.Context, host, _ string) (*deployment.ExecResult, error) {
		return failedResult([]string{host}, "  sh: lsblk: not found"), nil
	}
	ctrl := cacheTestController(t, dep)

	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "/dev/nvme0n1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lsblk")
}

func TestRefusesToAttachWithoutADevice(t *testing.T) {
	ctrl := cacheTestController(t, newAttachFake(""))
	_, err := ctrl.storage.AddPoolCache(context.Background(), "node-a", "sdspool", "  ", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}
