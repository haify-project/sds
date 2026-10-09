package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ResizeVolume is not atomic: it grows the backing LVs on every node and then
// resizes DRBD. DRBD refuses to resize a volume that is still doing its initial
// resync, so a resize issued right after create leaves the LVs already grown.
//
// `lvresize` EXITS NON-ZERO when the LV is already at the requested size, so
// without special handling that first partial failure would wedge the volume
// permanently: every retry dies at the LVM step and the DRBD side never catches
// up. Found on real hardware while validating the Proxmox plugin.

const resizeTestConfig = "resource res1 {\n    volume 0 {\n        device    minor 2;\n        disk      /dev/vg0/res1_data;\n        meta-disk internal;\n    }\n}\n"

// newResizeTestController wires a controller whose resource res1 has one volume
// backed by /dev/vg0/res1_data on node1+node2.
func newResizeTestController(t *testing.T, dep deploymentClient) *Controller {
	t.Helper()
	ctrl := newBasicTestController(dep)

	db := newTestDB(t)
	ctrl.db = db

	require.NoError(t, db.SaveResource(context.Background(), &database.Resource{
		Name: "res1", Port: 7001, Nodes: "node1,node2", Protocol: "C", Replicas: 2,
	}))
	require.NoError(t, db.SaveVolume(context.Background(), &database.Volume{
		ResourceName: "res1", VolumeName: "res1_data", VolumeID: 0,
		Pool: "vg0", SizeGB: 2, Device: "/dev/vg0/res1_data",
	}))
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})
	return ctrl
}

// resizeExecFunc builds an execFunc serving the .res config, and letting the
// caller decide how lvresize / lvs / drbdadm behave.
func resizeExecFunc(
	lvresize func(hosts []string) *deployment.ExecResult,
	lvs func(hosts []string) *deployment.ExecResult,
	drbd func(hosts []string) *deployment.ExecResult,
) func(context.Context, []string, string, ...deployment.ExecOption) (*deployment.ExecResult, error) {
	return func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		switch {
		case strings.HasPrefix(cmd, "cat /etc/drbd.d/res1.res"):
			return successExecResult(hosts, resizeTestConfig), nil
		case strings.HasPrefix(cmd, "sudo lvresize"):
			return lvresize(hosts), nil
		case strings.HasPrefix(cmd, "sudo lvs"):
			return lvs(hosts), nil
		case strings.HasPrefix(cmd, "sudo drbdadm resize"):
			return drbd(hosts), nil
		}
		return successExecResult(hosts, ""), nil
	}
}

// The retry case: lvresize refuses because the LVs are already 3 GB from the
// partially-applied first attempt. The resize must go through.
func TestResizeVolumeToleratesLVAlreadyAtTargetSize(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: resizeExecFunc(
			func(hosts []string) *deployment.ExecResult {
				return failedResult(hosts, "  New size (768 extents) matches existing size (768 extents).")
			},
			func(hosts []string) *deployment.ExecResult {
				return successExecResult(hosts, "  3221225472\n")
			},
			func(hosts []string) *deployment.ExecResult { return successExecResult(hosts, "") },
		),
	}
	ctrl := newResizeTestController(t, dep)

	require.NoError(t, ctrl.resources.ResizeVolume(context.Background(), "res1", 0, 3),
		"a retry after a partially-applied resize must complete, not fail forever")

	assert.True(t, execCmdIssued(dep, "sudo drbdadm resize res1/0"),
		"the DRBD side must still be resized on the retry")

	stored, err := ctrl.db.ListVolumes(context.Background(), "res1")
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, 3, stored[0].SizeGB, "metadata reflects the completed resize")
}

// A host whose LV is genuinely smaller than requested is a real failure and
// must not be swallowed by the tolerance above.
func TestResizeVolumeReportsHostGenuinelyBelowTarget(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: resizeExecFunc(
			func(hosts []string) *deployment.ExecResult {
				return failedResult(hosts, "Insufficient free space: 256 extents needed")
			},
			func(hosts []string) *deployment.ExecResult {
				// Both hosts are still at 2 GB.
				return successExecResult(hosts, "  2147483648\n")
			},
			func(hosts []string) *deployment.ExecResult { return successExecResult(hosts, "") },
		),
	}
	ctrl := newResizeTestController(t, dep)

	err := ctrl.resources.ResizeVolume(context.Background(), "res1", 0, 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.0.0.1")
	assert.Contains(t, err.Error(), "Insufficient free space",
		"the LVM error text must reach the operator, not just a host list")
	assert.False(t, execCmdIssued(dep, "sudo drbdadm resize"),
		"DRBD must not be resized when the backing volume was not grown")
}

// Mixed: one node already grown, one genuinely short. Only the short one is
// reported, and the resize still fails.
func TestResizeVolumeReportsOnlyTheShortHost(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: resizeExecFunc(
			func(hosts []string) *deployment.ExecResult { return failedResult(hosts, "lvresize failed") },
			func(hosts []string) *deployment.ExecResult {
				res := successExecResult(hosts, "  3221225472\n")
				res.Hosts["10.0.0.2"] = &deployment.HostResult{Host: "10.0.0.2", Success: true, Output: "  2147483648\n"}
				return res
			},
			func(hosts []string) *deployment.ExecResult { return successExecResult(hosts, "") },
		),
	}
	ctrl := newResizeTestController(t, dep)

	err := ctrl.resources.ResizeVolume(context.Background(), "res1", 0, 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.0.0.2")
	assert.NotContains(t, err.Error(), "10.0.0.1", "the already-grown host is not a failure")
}

// The DRBD step's own failure must explain itself: "resize failed on [ip]" gives
// the operator no clue that the volume is mid-resync and a retry will work.
func TestResizeVolumeSurfacesDRBDErrorOutput(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: resizeExecFunc(
			func(hosts []string) *deployment.ExecResult { return successExecResult(hosts, "") },
			func(hosts []string) *deployment.ExecResult { return successExecResult(hosts, "  3221225472\n") },
			func(hosts []string) *deployment.ExecResult {
				return failedResult(hosts, "Refusing to be resized: peer is Inconsistent")
			},
		),
	}
	ctrl := newResizeTestController(t, dep)

	err := ctrl.resources.ResizeVolume(context.Background(), "res1", 0, 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Refusing to be resized")
}

// The LV size probe failing must not be reported as success.
func TestResizeVolumeFailsWhenLVSizeCannotBeVerified(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: resizeExecFunc(
			func(hosts []string) *deployment.ExecResult { return failedResult(hosts, "lvresize failed") },
			func(hosts []string) *deployment.ExecResult { return failedResult(hosts, "no such volume group") },
			func(hosts []string) *deployment.ExecResult { return successExecResult(hosts, "") },
		),
	}
	ctrl := newResizeTestController(t, dep)

	require.Error(t, ctrl.resources.ResizeVolume(context.Background(), "res1", 0, 3))
}

func TestHostsBelowLVSizeParsing(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			res := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{
				"a": {Host: "a", Success: true, Output: "  3221225472\n"}, // exactly 3 GB
				"b": {Host: "b", Success: true, Output: "  4294967296\n"}, // bigger than asked
				"c": {Host: "c", Success: true, Output: "  2147483648\n"}, // short
				"d": {Host: "d", Success: true, Output: "not a number"},   // unreadable
				"e": {Host: "e", Success: false, Output: "command failed"},
			}}
			return res, nil
		},
	}
	ctrl := newResizeTestController(t, dep)

	short, err := ctrl.resources.hostsBelowLVSize(context.Background(),
		[]string{"a", "b", "c", "d", "e"}, "/dev/vg0/res1_data", 3)
	require.NoError(t, err)
	assert.Equal(t, []string{"c", "d", "e"}, short,
		"exact and larger sizes pass; short, unparsable and unreachable do not")
}

func TestFirstFailureOutput(t *testing.T) {
	assert.Equal(t, "", firstFailureOutput(nil))

	res := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{
		"a": {Host: "a", Success: true, Output: "fine"},
	}}
	assert.Equal(t, "no output", firstFailureOutput(res),
		"no failures means nothing to quote")

	res.Hosts["b"] = &deployment.HostResult{Host: "b", Success: false, Output: "  boom  \n"}
	assert.Equal(t, "boom", firstFailureOutput(res))
}
