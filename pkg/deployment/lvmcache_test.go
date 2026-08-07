package deployment

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Device paths reach ProbeBlockDevice straight from a gRPC request and end up
// inside a string handed to `sh -c`, so the quoting is the boundary between an
// operator naming a disk and an operator running a command.
func TestShellQuoteContainsOperatorInput(t *testing.T) {
	assert.Equal(t, "'/dev/nvme0n1'", shellQuote("/dev/nvme0n1"))
	assert.Equal(t, `'/dev/x'\''; rm -rf /; #'`, shellQuote("/dev/x'; rm -rf /; #"))
	assert.Equal(t, `'$(reboot)'`, shellQuote("$(reboot)"),
		"command substitution does not happen inside single quotes")
}

// The probe is one shell line built by hand, so it is run for real against a
// device that does not exist: this asserts that the line parses as a shell
// script and emits the key=value contract the controller parses, on whatever
// shell the test host has. Every lookup in it must survive a missing device and
// a missing tool without taking the whole command down with it.
func TestProbeBlockDeviceEmitsItsContractForAMissingDevice(t *testing.T) {
	c, host := covLocalClient(t)
	res, err := c.ProbeBlockDevice(covCtx(t), host, "/dev/sds-cache-probe-nodev")
	require.NoError(t, err)

	hr := res.Hosts[host]
	require.NotNil(t, hr)
	require.True(t, hr.Success, "a device that is simply absent must not fail the probe: %s", hr.Output)

	// "not a block device" has to be an answer, not an error.
	assert.Contains(t, hr.Output, "block=no")
	for _, key := range []string{"size=", "fstype=", "mount=", "holders=", "pvvg="} {
		assert.True(t, strings.Contains(hr.Output, key), "the probe must always report %q", key)
	}
}
