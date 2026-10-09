package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/deployment"
)

func TestParseLaSizeSectors(t *testing.T) {
	assert.Equal(t, uint64(5371715584), parseLaSizeSectors("10491632\n"))
	assert.Zero(t, parseLaSizeSectors(""), "no DRBD metadata on the source")
	assert.Zero(t, parseLaSizeSectors("garbage"))

	cmd := sourceDataBytesCmd("/dev/sds_vg0/pve-base-100-0_data")
	assert.Contains(t, cmd, "drbdmeta --force 1048575 v09 /dev/sds_vg0/pve-base-100-0_data internal dump-md")
	assert.Contains(t, cmd, "lvchange -ay -K sds_vg0/pve-base-100-0_data", "a snapshot source is activated first")
}

// The source's data region decides the copy, and a target smaller than it is
// grown before anything is written: a Proxmox template whose guest had grown
// its partition into the last MiB of the device cloned into a disk that did
// not boot.
func TestFitTargetToSource(t *testing.T) {
	run := func(laSize, targetBytes string) (uint64, []string, error) {
		dep := &fakeDeploymentClient{}
		dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			switch {
			case strings.Contains(cmd, "drbdmeta"):
				return successExecResult(hosts, laSize), nil
			case strings.Contains(cmd, "blockdev --getsize64"):
				return successExecResult(hosts, targetBytes), nil
			}
			return successExecResult(hosts, ""), nil
		}
		ctrl := newBasicTestController(dep)
		got, err := ctrl.snapshots.fitTargetToSource(context.Background(), "pve-101-0", 0, "10.0.0.1",
			"/dev/drbd/by-res/pve-101-0/0", "/dev/sds_vg0/pve-base-100-0_data")
		var cmds []string
		for _, c := range dep.execCalls {
			cmds = append(cmds, c.cmd)
		}
		return got, cmds, err
	}

	got, _, err := run("10491632", "5368709120")
	require.Error(t, err, "a 5 GiB target cannot take a 5 GiB + 3 MiB source without growing")
	assert.Contains(t, err.Error(), "growing it failed")
	assert.Zero(t, got)

	got, cmds, err := run("10485760", "5368709120")
	require.NoError(t, err)
	assert.Equal(t, uint64(5368709120), got, "the source's data region is what gets copied")
	for _, c := range cmds {
		assert.NotContains(t, c, "lvresize", "a target that fits is not grown")
	}

	got, _, err = run("", "5368709120")
	require.NoError(t, err)
	assert.Zero(t, got, "a source without DRBD metadata leaves the target's size in charge")
}
