package controller

import (
	"context"
	"testing"

	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Adding a replica to a node whose pool is thin used to run a plain `lvcreate
// -L`, which asks the volume group for space the thin pool already owns:
//
//	Volume group "sds_sdspool" has insufficient free space (70 extents): 257 required
//
// The node had 10 GiB of thin pool and 280 MiB of slack, so the request was
// impossible by construction rather than merely unlucky. Three call sites made
// the volume three different ways; this is the one they now share.

func TestBackingVolumeGoesIntoTheThinPoolWhenThereIsOne(t *testing.T) {
	var thinCall struct {
		vg, pool, lv, size string
		called             bool
	}
	dep := &fakeDeploymentClient{
		lvThinPoolInFunc: func(_ context.Context, _, vgName string) (string, error) {
			return vgName + "_thin", nil
		},
		lvCreateThinVolumeFunc: func(_ context.Context, hosts []string, vg, pool, lv, size string) (*deployment.ExecResult, error) {
			thinCall.vg, thinCall.pool, thinCall.lv, thinCall.size, thinCall.called = vg, pool, lv, size, true
			return successExecResult(hosts, ""), nil
		},
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, _ string) (*deployment.ExecResult, error) {
			t.Error("a thick lvcreate must not be used on a thin pool")
			return successExecResult(hosts, ""), nil
		},
	}
	rm := convertTestManager(dep)
	require.NoError(t, rm.createBackingVolumeOn(context.Background(), "10.0.0.1",
		"sds_sdspool", "sds-meta_data", 1077936128))

	require.True(t, thinCall.called)
	assert.Equal(t, "sds_sdspool", thinCall.vg)
	assert.Equal(t, "sds_sdspool_thin", thinCall.pool)
	assert.Equal(t, "sds-meta_data", thinCall.lv)
	// DRBD records the device size in its metadata; a replica built from a
	// rounded size is a different device and refuses to attach.
	assert.Equal(t, "1077936128B", thinCall.size)
}

// The pool a conversion produces and the pool `pool create` produces are named
// differently, so the name cannot be assumed — whatever the node reports is
// what gets used.
func TestBackingVolumeUsesWhateverTheThinPoolIsCalled(t *testing.T) {
	var got string
	dep := &fakeDeploymentClient{
		lvThinPoolInFunc: func(_ context.Context, _, _ string) (string, error) { return "sdsthin", nil },
		lvCreateThinVolumeFunc: func(_ context.Context, hosts []string, _, pool, _, _ string) (*deployment.ExecResult, error) {
			got = pool
			return successExecResult(hosts, ""), nil
		},
	}
	require.NoError(t, convertTestManager(dep).createBackingVolumeOn(context.Background(),
		"10.0.0.1", "sds_sdspool", "openclaw_data", 6442450944))
	assert.Equal(t, "sdsthin", got)
}

func TestBackingVolumeStaysThickWhenThePoolIsThick(t *testing.T) {
	var thick bool
	dep := &fakeDeploymentClient{
		lvThinPoolInFunc: func(_ context.Context, _, _ string) (string, error) { return "", nil },
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, size string) (*deployment.ExecResult, error) {
			thick = true
			assert.Equal(t, "1077936128B", size)
			return successExecResult(hosts, ""), nil
		},
		lvCreateThinVolumeFunc: func(_ context.Context, hosts []string, _, _, _, _ string) (*deployment.ExecResult, error) {
			t.Error("there is no thin pool to put it in")
			return successExecResult(hosts, ""), nil
		},
	}
	require.NoError(t, convertTestManager(dep).createBackingVolumeOn(context.Background(),
		"10.0.0.1", "sds_sdspool", "sds-meta_data", 1077936128))
	assert.True(t, thick)
}

// lvcreate exits non-zero while the SSH call itself succeeds, so the failure
// arrives in the result and not in the error.
func TestBackingVolumeReportsALVMFailure(t *testing.T) {
	dep := &fakeDeploymentClient{
		lvThinPoolInFunc: func(_ context.Context, _, _ string) (string, error) { return "", nil },
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, _ string) (*deployment.ExecResult, error) {
			return failedResult(hosts, "  Volume group \"sds_sdspool\" has insufficient free space"), nil
		},
	}
	err := convertTestManager(dep).createBackingVolumeOn(context.Background(),
		"10.0.0.1", "sds_sdspool", "sds-meta_data", 1077936128)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient free space")
}

// Resource creation must ask the node what shape the pool has, exactly as
// add-replica does. `--storage-type` defaults to "lvm" and nothing reconciles it
// with the pool it names, so before this a resource created in a thin pool
// without the flag attempted a thick lvcreate in a volume group the thin pool
// had already consumed — which cannot succeed by construction.
func TestCreateResourceUsesTheThinPoolEvenWhenStorageTypeSaysLVM(t *testing.T) {
	var thinPool string
	dep := &fakeDeploymentClient{
		lvThinPoolInFunc: func(_ context.Context, _, vgName string) (string, error) {
			return vgName + "_thin", nil
		},
		lvCreateThinVolumeFunc: func(_ context.Context, hosts []string, _, pool, _, _ string) (*deployment.ExecResult, error) {
			thinPool = pool
			return successExecResult(hosts, ""), nil
		},
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, _ string) (*deployment.ExecResult, error) {
			t.Error("a thick lvcreate must not be used on a thin pool")
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	require.NoError(t, ctrl.resources.createBackingVolume(context.Background(),
		[]string{"10.0.0.1"}, []string{"node1"}, "lvm", "vg0", "res1_data", 2, false))
	assert.Equal(t, "vg0_thin", thinPool)
}

// A genuinely thick pool still gets a thick volume.
func TestCreateResourceStaysThickWhenTheNodeHasNoThinPool(t *testing.T) {
	thick := false
	dep := &fakeDeploymentClient{
		lvThinPoolInFunc: func(_ context.Context, _, _ string) (string, error) { return "", nil },
		lvCreateFunc: func(_ context.Context, hosts []string, _, _, _ string) (*deployment.ExecResult, error) {
			thick = true
			return successExecResult(hosts, ""), nil
		},
		lvCreateThinVolumeFunc: func(_ context.Context, hosts []string, _, _, _, _ string) (*deployment.ExecResult, error) {
			t.Error("there is no thin pool to use")
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)

	require.NoError(t, ctrl.resources.createBackingVolume(context.Background(),
		[]string{"10.0.0.1"}, []string{"node1"}, "lvm", "vg0", "res1_data", 2, false))
	assert.True(t, thick)
}

// Asking for lvm-thin on a pool that has none is still an error, not a silent
// downgrade to a thick volume the operator did not ask for.
func TestCreateResourceRefusesThinOnAPoolWithoutOne(t *testing.T) {
	dep := &fakeDeploymentClient{
		lvThinPoolInFunc: func(_ context.Context, _, _ string) (string, error) { return "", nil },
	}
	ctrl := newBasicTestController(dep)

	err := ctrl.resources.createBackingVolume(context.Background(),
		[]string{"10.0.0.1"}, []string{"node1"}, "lvm-thin", "vg0", "res1_data", 2, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no thin pool")
}
