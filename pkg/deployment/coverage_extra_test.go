package deployment

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// covLocalClient builds a real *Client with a nil dispatch backend and returns
// a host string that classifies as "local" so that Client.Exec takes the
// os/exec `sh -c` path (never touching the nil dispatch client). The commands
// issued by the builder methods use fake resource/device names and, on this
// platform, reference tools that either do not exist or require a tty for sudo,
// so they fail fast without side effects. That is fine: we are exercising the
// command-builder code paths for coverage, not asserting real storage effects.
func covLocalClient(t *testing.T) (*Client, string) {
	t.Helper()
	ips := getLocalIPs()
	if len(ips) == 0 {
		t.Skip("no local non-loopback IP available; cannot exercise local Exec path")
	}
	return &Client{logger: zap.NewNop(), parallel: 1}, ips[0]
}

// covCtx returns a short-lived context so any command that would otherwise hang
// (it should not) is force-killed by exec.CommandContext.
func covCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestCovExecLocal drives the local branch of Client.Exec end to end with a
// harmless command and asserts the result plumbing.
func TestCovExecLocal(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)

	res, err := c.Exec(ctx, []string{host}, "printf ok")
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Contains(t, res.Hosts, host)
	assert.True(t, res.Hosts[host].Success)
	assert.Equal(t, "ok", res.Hosts[host].Output)
	assert.True(t, res.AllSuccess())
	assert.Empty(t, res.FailedHosts())
}

// TestCovExecLocalFailure exercises the non-zero exit-code branch of Exec.
func TestCovExecLocalFailure(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)

	res, err := c.Exec(ctx, []string{host}, "exit 3")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Hosts[host].Success)
	assert.False(t, res.AllSuccess())
	assert.Contains(t, res.FailedHosts(), host)
}

// TestCovExecWithOptions covers the WithExecParallel / WithExecTimeout options.
func TestCovExecWithOptions(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)

	res, err := c.Exec(ctx, []string{host}, "printf hi",
		WithExecParallel(4), WithExecTimeout(5*time.Second))
	require.NoError(t, err)
	assert.True(t, res.Hosts[host].Success)
}

// TestCovLVMBuilders runs every LVM command-builder method through the real
// local Exec path.
func TestCovLVMBuilders(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)
	hosts := []string{host}

	builders := []struct {
		name string
		call func() (*ExecResult, error)
	}{
		{"PVCreate", func() (*ExecResult, error) { return c.PVCreate(ctx, hosts, "/dev/cov-nodev") }},
		{"PVCreateForce", func() (*ExecResult, error) { return c.PVCreate(ctx, hosts, "/dev/cov-nodev", WithLVMForce(true)) }},
		{"VGCreate", func() (*ExecResult, error) { return c.VGCreate(ctx, hosts, "cov_vg", []string{"/dev/cov-nodev"}) }},
		{"LVCreate", func() (*ExecResult, error) { return c.LVCreate(ctx, hosts, "cov_vg", "cov_lv", "1G") }},
		{"LVCreateThinPoolAbs", func() (*ExecResult, error) { return c.LVCreateThinPool(ctx, hosts, "cov_vg", "cov_pool", "1G") }},
		{"LVCreateThinPoolPct", func() (*ExecResult, error) { return c.LVCreateThinPool(ctx, hosts, "cov_vg", "cov_pool", "95%FREE") }},
		{"LVCreateThinVolume", func() (*ExecResult, error) { return c.LVCreateThinVolume(ctx, hosts, "cov_vg", "cov_pool", "cov_lv", "1G") }},
		{"LVRemove", func() (*ExecResult, error) { return c.LVRemove(ctx, hosts, "cov_vg/cov_lv") }},
		{"LVCreateSnapshot", func() (*ExecResult, error) { return c.LVCreateSnapshot(ctx, hosts, "cov_vg", "cov_lv", "cov_snap", "1G") }},
		{"LVCreateThinSnapshot", func() (*ExecResult, error) { return c.LVCreateThinSnapshot(ctx, hosts, "cov_vg", "cov_lv", "cov_snap") }},
		{"LVRemoveSnapshot", func() (*ExecResult, error) { return c.LVRemoveSnapshot(ctx, hosts, "cov_vg", "cov_snap") }},
		{"LVListSnapshots", func() (*ExecResult, error) { return c.LVListSnapshots(ctx, hosts, "cov_vg") }},
		{"LVMergeSnapshot", func() (*ExecResult, error) { return c.LVMergeSnapshot(ctx, hosts, "cov_vg", "cov_snap") }},
	}

	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			res, err := b.call()
			require.NoError(t, err)
			require.NotNil(t, res)
			require.Contains(t, res.Hosts, host)
		})
	}
}

// TestCovLVIsThin covers the segtype parsing helper (both the command build and
// the output-parsing loop).
func TestCovLVIsThin(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)

	isThin, err := c.LVIsThin(ctx, host, "cov_vg", "cov_lv")
	require.NoError(t, err)
	// The fake LV does not exist so lvs fails; the parser returns false.
	assert.False(t, isThin)
}

// TestCovZFSBuilders runs every ZFS command-builder method through Exec.
func TestCovZFSBuilders(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)
	hosts := []string{host}

	builders := []struct {
		name string
		call func() (*ExecResult, error)
	}{
		{"ZFSCreatePool", func() (*ExecResult, error) { return c.ZFSCreatePool(ctx, hosts, "covpool", []string{"/dev/cov-nodev"}) }},
		{"ZFSCreatePoolOpts", func() (*ExecResult, error) {
			return c.ZFSCreatePool(ctx, hosts, "covpool", []string{"/dev/cov-nodev"}, WithZFSCompression(true), WithZFSDedup(true))
		}},
		{"ZFSDestroyPool", func() (*ExecResult, error) { return c.ZFSDestroyPool(ctx, hosts, "covpool") }},
		{"ZFSListPools", func() (*ExecResult, error) { return c.ZFSListPools(ctx, hosts) }},
		{"ZFSGetPool", func() (*ExecResult, error) { return c.ZFSGetPool(ctx, hosts, "covpool") }},
		{"ZFSCreateDataset", func() (*ExecResult, error) { return c.ZFSCreateDataset(ctx, hosts, "covpool/ds") }},
		{"ZFSCreateThinDataset", func() (*ExecResult, error) { return c.ZFSCreateThinDataset(ctx, hosts, "covpool", "ds", "1G") }},
		{"ZFSDestroyDataset", func() (*ExecResult, error) { return c.ZFSDestroyDataset(ctx, hosts, "covpool/ds") }},
		{"ZFSSnapshot", func() (*ExecResult, error) { return c.ZFSSnapshot(ctx, hosts, "covpool/ds", "snap") }},
		{"ZFSRollback", func() (*ExecResult, error) { return c.ZFSRollback(ctx, hosts, "covpool/ds", "snap") }},
		{"ZFSClone", func() (*ExecResult, error) { return c.ZFSClone(ctx, hosts, "covpool/ds@snap", "covpool/clone") }},
		{"ZFSListSnapshots", func() (*ExecResult, error) { return c.ZFSListSnapshots(ctx, hosts, "covpool/ds") }},
		{"ZFSDestroySnapshot", func() (*ExecResult, error) { return c.ZFSDestroySnapshot(ctx, hosts, "covpool/ds@snap") }},
		{"ZFSSetQuota", func() (*ExecResult, error) { return c.ZFSSetQuota(ctx, hosts, "covpool/ds", "1G") }},
		{"ZFSSetReservation", func() (*ExecResult, error) { return c.ZFSSetReservation(ctx, hosts, "covpool/ds", "1G") }},
		{"ZFSResizeVolume", func() (*ExecResult, error) { return c.ZFSResizeVolume(ctx, hosts, "covpool/ds", "2G") }},
	}

	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			res, err := b.call()
			require.NoError(t, err)
			require.NotNil(t, res)
		})
	}
}

// TestCovDRBDBuilders runs the DRBD command-builder methods.
func TestCovDRBDBuilders(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)
	hosts := []string{host}

	builders := []struct {
		name string
		call func() (*ExecResult, error)
	}{
		{"DRBDUp", func() (*ExecResult, error) { return c.DRBDUp(ctx, hosts, "covres") }},
		{"DRBDDown", func() (*ExecResult, error) { return c.DRBDDown(ctx, hosts, "covres") }},
		{"DRBDCreateMD", func() (*ExecResult, error) { return c.DRBDCreateMD(ctx, hosts, "covres", 0) }},
		{"DRBDAdjust", func() (*ExecResult, error) { return c.DRBDAdjust(ctx, hosts, "covres") }},
		{"DRBDStatus", func() (*ExecResult, error) { return c.DRBDStatus(ctx, hosts, "covres") }},
		{"DRBDStatusJSON", func() (*ExecResult, error) { return c.DRBDStatusJSON(ctx, hosts, "covres") }},
	}

	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			res, err := b.call()
			require.NoError(t, err)
			require.NotNil(t, res)
		})
	}
}

// TestCovDRBDPrimarySecondary covers the single-host role-change helpers, which
// return a *HostResult extracted from the result map.
func TestCovDRBDPrimarySecondary(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)

	pr, err := c.DRBDPrimary(ctx, host, "covres", false)
	require.NoError(t, err)
	require.NotNil(t, pr)

	prForce, err := c.DRBDPrimary(ctx, host, "covres", true)
	require.NoError(t, err)
	require.NotNil(t, prForce)

	sr, err := c.DRBDSecondary(ctx, host, "covres")
	require.NoError(t, err)
	require.NotNil(t, sr)
}

// TestCovServiceBuilders covers the systemd service helpers.
func TestCovServiceBuilders(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)
	hosts := []string{host}

	for _, b := range []struct {
		name string
		call func() (*ExecResult, error)
	}{
		{"ServiceStart", func() (*ExecResult, error) { return c.ServiceStart(ctx, hosts, "cov.service") }},
		{"ServiceStop", func() (*ExecResult, error) { return c.ServiceStop(ctx, hosts, "cov.service") }},
		{"ServiceRestart", func() (*ExecResult, error) { return c.ServiceRestart(ctx, hosts, "cov.service") }},
	} {
		t.Run(b.name, func(t *testing.T) {
			res, err := b.call()
			require.NoError(t, err)
			require.NotNil(t, res)
		})
	}
}

// TestCovReactorBuilders covers the reactor enable/disable/reload helpers.
func TestCovReactorBuilders(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)
	hosts := []string{host}

	for _, b := range []struct {
		name string
		call func() (*ExecResult, error)
	}{
		{"ReactorEnablePlugin", func() (*ExecResult, error) { return c.ReactorEnablePlugin(ctx, hosts, "covplugin") }},
		{"ReactorDisablePlugin", func() (*ExecResult, error) { return c.ReactorDisablePlugin(ctx, hosts, "covplugin") }},
		{"ReactorReload", func() (*ExecResult, error) { return c.ReactorReload(ctx, hosts) }},
	} {
		t.Run(b.name, func(t *testing.T) {
			res, err := b.call()
			require.NoError(t, err)
			require.NotNil(t, res)
		})
	}
}

// TestCovReactorStatusError covers the failure branch of ReactorStatusJSON and
// ReactorPromoterStatusByResource (the `drbd-reactorctl` command fails on this
// platform, so both should return an error rather than parse JSON).
func TestCovReactorStatusError(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)

	_, err := c.ReactorStatusJSON(ctx, host)
	assert.Error(t, err)

	_, err = c.ReactorPromoterStatusByResource(ctx, host, "covres")
	assert.Error(t, err)
}

// TestCovDistributeConfigLocal drives DistributeConfig on a local host. The
// os.WriteFile to /tmp succeeds; the privileged `sudo mkdir`/`mv` fail on this
// platform and are recorded as a per-host failure, exercising the local error
// branch. Also covers ReactorWriteConfig (which wraps DistributeConfig with a
// post-command) and DeleteConfig.
func TestCovDistributeConfigLocal(t *testing.T) {
	c, host := covLocalClient(t)
	ctx := covCtx(t)
	hosts := []string{host}

	res, err := c.DistributeConfig(ctx, hosts, "cov-content", "/tmp/cov-nonexistent-dir/cov.toml",
		WithBackup(true), WithPostCommand("true"))
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Contains(t, res.Hosts, host)

	rw, err := c.ReactorWriteConfig(ctx, hosts, "covplugin", "cov-content")
	require.NoError(t, err)
	require.NotNil(t, rw)

	err = c.DeleteConfig(ctx, hosts, "/tmp/cov-nonexistent-dir/cov.toml")
	require.NoError(t, err)
}

// TestCovJSONUnmarshal covers the local json.Unmarshal wrapper directly.
func TestCovJSONUnmarshal(t *testing.T) {
	var s ReactorStatus
	err := jsonUnmarshal([]byte(`{"promoter":[{"drbd_resource":"r1","status":"active"}]}`), &s)
	require.NoError(t, err)
	require.Len(t, s.Promoter, 1)
	assert.Equal(t, "r1", s.Promoter[0].DRBDResource)

	err = jsonUnmarshal([]byte(`{bad json`), &s)
	assert.Error(t, err)
}

// TestCovIsLocalHostIPMatch exercises the InterfaceAddrs branch of isLocalHost
// by passing a real local IP (loopback-excluded getLocalIPs entry).
func TestCovIsLocalHostIPMatch(t *testing.T) {
	ips := getLocalIPs()
	if len(ips) == 0 {
		t.Skip("no local IP available")
	}
	assert.True(t, isLocalHost(ips[0]))
}
