package controller

import (
	"context"
	"testing"

	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newDetachFake(reports ...string) *fakeDeploymentClient {
	return &fakeDeploymentClient{lvsCacheReportFunc: reportSequence(reports...)}
}

func TestRemovesACacheAndReleasesItsDevice(t *testing.T) {
	dep := newDetachFake(cachedThinPoolReport("writeback", "Cwi-aoC---"), "")
	var uncachedLV, releasedDevice string
	dep.lvUncacheFunc = func(_ context.Context, hosts []string, _, lv string) (*deployment.ExecResult, error) {
		uncachedLV = lv
		return successExecResult(hosts, ""), nil
	}
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, device string) (*deployment.ExecResult, error) {
		releasedDevice = device
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	require.NoError(t, ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool"))
	// lvconvert takes the pool, not the internal sub-LV the cache is bolted to.
	assert.Equal(t, "sds_sdspool_thin", uncachedLV)
	// Leaving the SSD in the group would make it free space that the next
	// thin-pool extension would allocate pool data onto.
	assert.Equal(t, "/dev/nvme0n1", releasedDevice)
}

func TestRefusesToRemoveACacheThatIsNotThere(t *testing.T) {
	ctrl := cacheTestController(t, newDetachFake(""))
	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no cache")
}

// A cache whose device is gone cannot be flushed. Detaching it anyway needs
// --force, which discards the dirty blocks — a decision that belongs to whoever
// owns the data, not to this command.
func TestRefusesToRemoveACacheThatCannotBeFlushed(t *testing.T) {
	dep := newDetachFake(cachedThinPoolReport("writeback", "Cwi-aoC-p-"))
	var uncalled = true
	dep.lvUncacheFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		uncalled = false
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
	assert.True(t, uncalled, "nothing may be detached when the flush is known to be impossible")
}

// The flush is what makes the SSD safe to pull, and lvconvert reporting success
// is not the same fact as the cache being gone.
func TestDoesNotReportSuccessWhenTheCacheIsStillAttached(t *testing.T) {
	stillThere := cachedThinPoolReport("writeback", "Cwi-aoC---")
	dep := newDetachFake(stillThere, stillThere)
	var released bool
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		released = true
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still attached")
	assert.False(t, released, "the device must not be pulled out from under a cache that is still there")
}

func TestReportsAFailedFlush(t *testing.T) {
	dep := newDetachFake(cachedThinPoolReport("writeback", "Cwi-aoC---"), "")
	dep.lvUncacheFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		return failedResult(hosts, "  Failed to flush cache, device is not writable"), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Failed to flush cache")
}

// A device that could not be released is still a member PV of the pool, which
// the operator has to know about — but the flush did happen, and the message
// has to say so rather than implying the data is at risk.
func TestReportsADeviceThatCouldNotBeReleased(t *testing.T) {
	dep := newDetachFake(cachedThinPoolReport("writethrough", "Cwi-aoC---"), "")
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		return failedResult(hosts, "  Physical volume /dev/nvme0n1 still in use"), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "flushed and detached")
	assert.Contains(t, err.Error(), "still in use")
}

// If lvs never named the fast device, it is still a member PV of the pool after
// the detach — the same end state as a vgreduce that failed, and reported the
// same way rather than passed over.
func TestReportsACacheDeviceItCouldNotIdentify(t *testing.T) {
	anonymous := lvsLine("sds_sdspool", "[sds_sdspool_thin_tdata]", "Cwi-aoC---", "cache", "107374182400",
		"writethrough", "1638400", "409600", "0", "1", "1", "1", "1", "sds_sdspool_thin_tdata_corig(0)")
	dep := newDetachFake(anonymous, "")
	var released bool
	dep.vgReduceAndRemovePVFunc = func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
		released = true
		return successExecResult(hosts, ""), nil
	}
	ctrl := cacheTestController(t, dep)

	err := ctrl.storage.RemovePoolCache(context.Background(), "node-a", "sdspool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vgreduce")
	assert.False(t, released, "there is no device name to hand to vgreduce")
}
