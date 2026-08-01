package deployment

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An error naming only the hosts that failed ("failed on hosts: [10.0.0.1
// 10.0.0.2]") makes the operator SSH in and replay the command by hand, because
// the reason drbdadm/lvcreate/zfs printed is dropped. FailureDetails keeps it.
func TestFailureDetailsIncludesReason(t *testing.T) {
	res := &ExecResult{Hosts: map[string]*HostResult{
		"10.0.0.2": {Host: "10.0.0.2", Output: "  Volume group \"vg0\" not found", Success: false},
		"10.0.0.1": {Host: "10.0.0.1", Output: "Device '/dev/sdb' not found", Success: false},
		"10.0.0.3": {Host: "10.0.0.3", Output: "ok", Success: true},
	}}

	got := res.FailureDetails()
	// Hosts are sorted so the message is stable run to run.
	assert.Equal(t,
		`10.0.0.1: Device '/dev/sdb' not found; 10.0.0.2: Volume group "vg0" not found`,
		got)
	assert.NotContains(t, got, "10.0.0.3", "successful hosts must not appear")
}

func TestFailureDetailsEmptyWhenAllSucceed(t *testing.T) {
	res := &ExecResult{Hosts: map[string]*HostResult{
		"10.0.0.1": {Host: "10.0.0.1", Output: "fine", Success: true},
	}}
	assert.Empty(t, res.FailureDetails())
	assert.True(t, res.AllSuccess())
}

// A command can fail with nothing on either stream (killed, connection lost).
// Say so explicitly instead of rendering "10.0.0.1: ".
func TestFailureDetailsFallsBackToErrorThenPlaceholder(t *testing.T) {
	res := &ExecResult{Hosts: map[string]*HostResult{
		"10.0.0.1": {Host: "10.0.0.1", Success: false, Error: errors.New("connection reset")},
		"10.0.0.2": {Host: "10.0.0.2", Success: false},
	}}
	got := res.FailureDetails()
	assert.Contains(t, got, "10.0.0.1: connection reset")
	assert.Contains(t, got, "10.0.0.2: no output")
}

// Multi-line tool output is collapsed so the error stays greppable on one line.
func TestFailureDetailsCollapsesWhitespace(t *testing.T) {
	res := &ExecResult{Hosts: map[string]*HostResult{
		"10.0.0.1": {Host: "10.0.0.1", Success: false, Output: "line one\n  line two\n\nline three"},
	}}
	assert.Equal(t, "10.0.0.1: line one line two line three", res.FailureDetails())
}

// Config distribution reports failures the same way; it used to surface a bare
// "config distribution failed on some hosts" with no path and no reason.
func TestConfigResultFailureDetails(t *testing.T) {
	res := &ConfigResult{Path: "/etc/drbd.d/data.res", Hosts: map[string]*HostResult{
		"10.0.0.1": {Host: "10.0.0.1", Success: false, Output: "mkdir failed"},
		"10.0.0.2": {Host: "10.0.0.2", Success: true},
	}}
	assert.Equal(t, []string{"10.0.0.1"}, res.FailedHosts())
	assert.Equal(t, "10.0.0.1: mkdir failed", res.FailureDetails())
}

// The dispatch config path is only useful if it is actually honoured: before it
// was wired the controller silently kept reading ~/.dispatch/config.toml, so a
// non-root controller failed with opaque SSH auth errors while its configured
// file sat unused.
func TestNewWithOptionsUsesConfiguredDispatchPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "dispatch.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
[ssh]
user = "root"
port = 22

[hosts."10.0.0.1"]
addresses = ["10.0.0.1"]
`), 0o600))

	client, err := NewWithOptions(nil, Options{ConfigPath: cfgPath, Parallel: 3})
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, 3, client.parallel)
}

// Parallel 0 must not mean "no fan-out".
func TestNewWithOptionsDefaultsParallel(t *testing.T) {
	client, err := NewWithOptions(nil, Options{})
	require.NoError(t, err)
	assert.Equal(t, defaultParallel, client.parallel)
}

// A bad path must surface rather than silently falling back to the default
// config, which would reintroduce the "why is it using the wrong key" hunt.
func TestNewWithOptionsRejectsUnreadableConfig(t *testing.T) {
	_, err := NewWithOptions(nil, Options{ConfigPath: filepath.Join(t.TempDir(), "missing.toml")})
	assert.Error(t, err)
}
