package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newSelfHaTestController builds a controller with a real temp database and
// two registered nodes, the first named after the local hostname so the
// self-HA flow identifies it as "self".
func newSelfHaTestController(t *testing.T, dep *fakeDeploymentClient) (*Controller, string, string) {
	t.Helper()

	ctrl := newBasicTestController(dep)
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctrl.db = db

	hostname, err := os.Hostname()
	require.NoError(t, err)

	_, err = ctrl.nodes.RegisterNode(context.Background(), hostname, "10.0.0.1")
	require.NoError(t, err)
	_, err = ctrl.nodes.RegisterNode(context.Background(), "node2", "10.0.0.2")
	require.NoError(t, err)

	return ctrl, hostname, "10.0.0.1"
}

// withSelfHaFixtureFiles points the local artifact paths at temp fixtures.
func withSelfHaFixtureFiles(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "controller.toml")
	unit := filepath.Join(dir, "sds-controller.service")
	require.NoError(t, os.WriteFile(cfg, []byte("[server]\nport = 3374\n"), 0o644))
	require.NoError(t, os.WriteFile(unit, []byte("[Service]\nExecStart=/opt/sds/bin/sds-controller\n"), 0o644))

	origCfg, origUnit := controllerConfigPath, controllerUnitPath
	controllerConfigPath, controllerUnitPath = cfg, unit
	t.Cleanup(func() { controllerConfigPath, controllerUnitPath = origCfg, origUnit })
}

func selfHaFakeDeployment() *fakeDeploymentClient {
	return &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if cmd == "hostname" {
				out := make(map[string]*deployment.HostResult, len(hosts))
				for _, h := range hosts {
					out[h] = &deployment.HostResult{Host: h, Success: true, Output: h + ".local\n"}
				}
				return &deployment.ExecResult{Hosts: out}, nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
}

func TestEnableSelfHaValidation(t *testing.T) {
	dep := selfHaFakeDeployment()
	ctrl, _, _ := newSelfHaTestController(t, dep)

	// VIP must be CIDR.
	_, err := ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50", "p0", 0, 0, nil)
	assert.ErrorContains(t, err, "CIDR")

	// Pool is required.
	_, err = ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "", 0, 0, nil)
	assert.ErrorContains(t, err, "pool is required")

	// Unknown node.
	_, err = ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "p0", 0, 0, []string{"ghost"})
	assert.ErrorContains(t, err, "not registered")
}

func TestEnableSelfHaRequiresTwoNodes(t *testing.T) {
	dep := selfHaFakeDeployment()
	ctrl := newBasicTestController(dep)
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctrl.db = db

	hostname, err := os.Hostname()
	require.NoError(t, err)
	_, err = ctrl.nodes.RegisterNode(context.Background(), hostname, "10.0.0.1")
	require.NoError(t, err)

	_, err = ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "p0", 0, 0, nil)
	assert.ErrorContains(t, err, "at least 2 nodes")
}

func TestEnableSelfHaRejectsExistingResource(t *testing.T) {
	dep := selfHaFakeDeployment()
	ctrl, _, _ := newSelfHaTestController(t, dep)

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:  SelfHaResource,
		Nodes: "node1,node2",
	}))

	_, err := ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "p0", 0, 0, nil)
	assert.ErrorContains(t, err, "already exists")
}

func TestEnableSelfHaOrchestration(t *testing.T) {
	withSelfHaFixtureFiles(t)
	dep := selfHaFakeDeployment()
	ctrl, _, selfAddr := newSelfHaTestController(t, dep)

	logPath, err := ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "p0", 0, 0, nil)
	require.NoError(t, err)
	assert.Equal(t, selfHaHandoffLog, logPath)

	// The reactor promoter config must be distributed DISABLED to all nodes
	// and must wire mount + VIP + controller service.
	var reactorCfg, handoffScript string
	var reactorHosts []string
	for _, dc := range dep.distributedConfigs {
		switch dc.remotePath {
		case selfHaReactorConfig + ".disabled":
			reactorCfg = dc.content
			reactorHosts = dc.hosts
		case selfHaHandoffScript:
			handoffScript = dc.content
			assert.Equal(t, []string{selfAddr}, dc.hosts, "handoff script must go to the controller node only")
		case selfHaReactorConfig:
			t.Fatalf("reactor config must never be distributed enabled by the controller")
		}
	}
	require.NotEmpty(t, reactorCfg, "reactor config not distributed")
	assert.ElementsMatch(t, []string{selfAddr, "10.0.0.2"}, reactorHosts)
	assert.Contains(t, reactorCfg, "var-lib-sds.mount")
	assert.Contains(t, reactorCfg, "service-ip@10.0.0.50-24.service")
	assert.Contains(t, reactorCfg, "sds-controller.service")
	assert.Contains(t, reactorCfg, "[promoter.resources."+SelfHaResource+"]")

	// The handoff script must copy the DB before enabling reactor configs,
	// enable locally before the standbys, and roll back on failure.
	require.NotEmpty(t, handoffScript, "handoff script not distributed")
	dbCopy := strings.Index(handoffScript, "cp -a \"$DB_DIR/sds.db\"")
	localEnable := strings.Index(handoffScript, "mv \"$REACTOR_CONF.disabled\" \"$REACTOR_CONF\"")
	standbyEnable := strings.Index(handoffScript, selfHaSSHOpts+" 10.0.0.2 'sudo mv")
	require.Greater(t, dbCopy, 0)
	require.Greater(t, localEnable, dbCopy, "DB must be copied before enabling reactor management")
	require.Greater(t, standbyEnable, localEnable, "local node must take over before standbys are enabled")
	assert.Contains(t, handoffScript, "rollback()")
	assert.Contains(t, handoffScript, "systemctl stop sds-controller")
	// Failover means the script may run on a node with empty known_hosts.
	assert.Contains(t, handoffScript, "StrictHostKeyChecking=accept-new")

	// The mount unit must reach every node.
	foundMountUnit := false
	for _, dc := range dep.distributedConfigs {
		if dc.remotePath == "/etc/systemd/system/var-lib-sds.mount" {
			foundMountUnit = true
			assert.ElementsMatch(t, []string{selfAddr, "10.0.0.2"}, dc.hosts)
			assert.Contains(t, dc.content, "/dev/drbd/by-res/"+SelfHaResource+"/0")
		}
	}
	assert.True(t, foundMountUnit, "mount unit not distributed")

	// HA config must be persisted for `ha status sds-meta`.
	haCfg, err := ctrl.db.GetHaConfig(context.Background(), SelfHaResource)
	require.NoError(t, err)
	require.NotNil(t, haCfg)
	assert.Equal(t, "10.0.0.50/24", haCfg.VIP)
	assert.Equal(t, selfHaMountPoint, haCfg.MountPoint)

	// The detached handoff must be launched via systemd-run on this node.
	foundLaunch := false
	for _, call := range dep.execCalls {
		if strings.Contains(call.cmd, "systemd-run --unit=sds-selfha-handoff") {
			foundLaunch = true
			assert.Equal(t, []string{selfAddr}, call.hosts)
		}
	}
	assert.True(t, foundLaunch, "handoff was not launched via systemd-run")
}

func TestEnableSelfHaPreflightRejectsRunningStandbyController(t *testing.T) {
	withSelfHaFixtureFiles(t)
	dep := selfHaFakeDeployment()
	base := dep.execFunc
	dep.execFunc = func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "systemctl is-active --quiet sds-controller &&") {
			return successExecResult(hosts, "RUNNING\n"), nil
		}
		return base(ctx, hosts, cmd, opts...)
	}
	ctrl, _, _ := newSelfHaTestController(t, dep)

	_, err := ctrl.resources.EnableSelfHa(context.Background(), "10.0.0.50/24", "p0", 0, 0, nil)
	assert.ErrorContains(t, err, "already running")
}

func TestDisableSelfHa(t *testing.T) {
	dep := selfHaFakeDeployment()
	ctrl, hostname, selfAddr := newSelfHaTestController(t, dep)

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:  SelfHaResource,
		Nodes: fmt.Sprintf("%s,node2", hostname),
	}))
	require.NoError(t, ctrl.db.SaveHaConfig(context.Background(), &database.HaConfig{
		Resource: SelfHaResource,
		VIP:      "10.0.0.50/24",
	}))

	require.NoError(t, ctrl.resources.DisableSelfHa(context.Background(), "node2"))

	// The HA config record must be gone (deleted on the DRBD-hosted DB
	// before it is copied back out).
	haCfg, _ := ctrl.db.GetHaConfig(context.Background(), SelfHaResource)
	assert.Nil(t, haCfg)

	// Disable script distributed to self, restoring onto the remote target.
	var script string
	for _, dc := range dep.distributedConfigs {
		if dc.remotePath == selfHaDisableScript {
			script = dc.content
			assert.Equal(t, []string{selfAddr}, dc.hosts)
		}
	}
	require.NotEmpty(t, script, "disable script not distributed")
	assert.Contains(t, script, "rm -f \"$REACTOR_CONF\"")
	assert.Contains(t, script, selfHaSSHOpts+" 10.0.0.2 \"$restore_cmds\"")
	assert.Contains(t, script, "systemctl enable --now sds-controller")
	// Passive nodes lose their configs BEFORE the active node tears down
	// local management, so partial failures leave the controller running.
	remoteRm := strings.Index(script, selfHaSSHOpts+" 10.0.0.2 'sudo rm -f")
	localRm := strings.Index(script, "\nrm -f \"$REACTOR_CONF\"")
	require.Greater(t, remoteRm, 0)
	require.Greater(t, localRm, remoteRm, "remote configs must be removed before local teardown")
	// Removing a reactor plugin config does not reliably stop units it
	// started: the target must be stopped explicitly, and failures must
	// restore reactor management from the backup.
	assert.Contains(t, script, "systemctl stop \"$TARGET_UNIT\"")
	assert.Contains(t, script, "rollback()")
	assert.Contains(t, script, "CONF_BACKUP")

	foundLaunch := false
	for _, call := range dep.execCalls {
		if strings.Contains(call.cmd, "systemd-run --unit=sds-selfha-disable") {
			foundLaunch = true
		}
	}
	assert.True(t, foundLaunch, "disable was not launched via systemd-run")
}

func TestDisableSelfHaRequiresEnabledState(t *testing.T) {
	dep := selfHaFakeDeployment()
	ctrl, _, _ := newSelfHaTestController(t, dep)

	err := ctrl.resources.DisableSelfHa(context.Background(), "node2")
	assert.ErrorContains(t, err, "not enabled")
}

// TestDisableSelfHaRetryWithoutHaConfig covers the retry path: a previously
// failed disable already deleted the HA config record, and the operation
// must still be runnable as long as the metadata resource exists.
func TestDisableSelfHaRetryWithoutHaConfig(t *testing.T) {
	dep := selfHaFakeDeployment()
	ctrl, hostname, _ := newSelfHaTestController(t, dep)

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:  SelfHaResource,
		Nodes: fmt.Sprintf("%s,node2", hostname),
	}))
	// No HaConfig record on purpose.

	require.NoError(t, ctrl.resources.DisableSelfHa(context.Background(), "node2"))

	foundLaunch := false
	for _, call := range dep.execCalls {
		if strings.Contains(call.cmd, "systemd-run --unit=sds-selfha-disable") {
			foundLaunch = true
		}
	}
	assert.True(t, foundLaunch, "disable retry was not launched")
}

func TestGetSelfHaStatus(t *testing.T) {
	dep := selfHaFakeDeployment()
	ctrl, hostname, _ := newSelfHaTestController(t, dep)

	status, err := ctrl.resources.GetSelfHaStatus(context.Background())
	require.NoError(t, err)
	assert.False(t, status.Enabled)

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:  SelfHaResource,
		Nodes: fmt.Sprintf("%s,node2", hostname),
	}))
	require.NoError(t, ctrl.db.SaveHaConfig(context.Background(), &database.HaConfig{
		Resource: SelfHaResource,
		VIP:      "10.0.0.50/24",
	}))

	status, err = ctrl.resources.GetSelfHaStatus(context.Background())
	require.NoError(t, err)
	assert.True(t, status.Enabled)
	assert.Equal(t, SelfHaResource, status.Resource)
	assert.Equal(t, "10.0.0.50/24", status.VIP)
	assert.ElementsMatch(t, []string{hostname, "node2"}, status.Nodes)
}

func TestSelfHaDisableScriptLocalTarget(t *testing.T) {
	script := generateSelfHaDisableScript([]string{"10.0.0.2"}, "10.0.0.1", "10.0.0.1", "10.0.0.50/24")
	// Restoring on the script's own node must not go through SSH.
	assert.NotContains(t, script, selfHaSSHOpts+" 10.0.0.1")
	assert.Contains(t, script, "bash -c \"${restore_cmds//sudo /}\"")
	// The other node still gets its reactor config removed remotely.
	assert.Contains(t, script, selfHaSSHOpts+" 10.0.0.2 'sudo rm -f")
}

// TestSelfHaDisableScriptCleansReactorDropins guards against the stale
// drop-in trap: drbd-reactor's generated unit overrides survive config
// removal, and starting the standalone controller with them in place pulls
// the whole promote/mount/VIP chain back up.
func TestSelfHaDisableScriptCleansReactorDropins(t *testing.T) {
	script := generateSelfHaDisableScript([]string{"10.0.0.2"}, "10.0.0.2", "10.0.0.1", "10.0.0.50/24")
	assert.Contains(t, script, "/run/systemd/system/sds-controller.service.d")
	assert.Contains(t, script, "/run/systemd/system/var-lib-sds.mount.d")
	assert.Contains(t, script, `drbd-promote@sds\x2dmeta.service.d`)
	assert.Contains(t, script, `drbd-services@sds\x2dmeta.target.d`)
	assert.Contains(t, script, "service-ip@10.0.0.50-24.service.d")
	assert.Contains(t, script, "rm -f /etc/systemd/system/var-lib-sds.mount")
	assert.Contains(t, script, "systemctl daemon-reload")
	// Remote nodes get the same cleanup over SSH.
	assert.Contains(t, script, "sudo rm -rf /run/systemd/system/sds-controller.service.d")

	// Without a known VIP (disable retry), the VIP drop-in is skipped but
	// everything else is still cleaned.
	script = generateSelfHaDisableScript([]string{"10.0.0.2"}, "10.0.0.2", "10.0.0.1", "")
	assert.NotContains(t, script, "service-ip@10.0.0.50-24.service.d")
	assert.Contains(t, script, "/run/systemd/system/sds-controller.service.d")
}

// TestAutoSelectPool covers the no-pool-given path of resource creation:
// unambiguous with one registered pool, explicit errors otherwise.
func TestAutoSelectPool(t *testing.T) {
	dep := selfHaFakeDeployment()
	ctrl, _, _ := newSelfHaTestController(t, dep)

	_, err := ctrl.resources.autoSelectPool(context.Background())
	assert.ErrorContains(t, err, "no pools are registered")

	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{Name: "sds_vg0", Node: "node1"}))
	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{Name: "sds_vg0", Node: "node2"}))
	name, err := ctrl.resources.autoSelectPool(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sds_vg0", name)

	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{Name: "sds_other", Node: "node1"}))
	_, err = ctrl.resources.autoSelectPool(context.Background())
	assert.ErrorContains(t, err, "multiple pools")
}

func TestParseDeviceMinor(t *testing.T) {
	cases := []struct {
		line string
		want int
		ok   bool
	}{
		{"        device    minor 2;", 2, true},
		{"device minor 999;", 999, true},
		{"  device /dev/drbd7 minor 7;", 7, true},
		{"    disk      /dev/vg/lv;", 0, false},
		{"minor 3;", 0, false},
	}
	for _, c := range cases {
		got, ok := parseDeviceMinor(c.line)
		assert.Equal(t, c.ok, ok, c.line)
		if ok {
			assert.Equal(t, c.want, got, c.line)
		}
	}
}

// TestNextGlobalMinorScansAllResources guards minor allocation: minors are a
// node-global namespace, so the next free one must account for every
// resource config, not just the resource being modified.
func TestNextGlobalMinorScansAllResources(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "cat /etc/drbd.d/*.res") {
				return successExecResult(hosts,
					"resource a {\n    volume 0 {\n        device    minor 0;\n    }\n}\n"+
						"resource b {\n    volume 0 {\n        device    minor 999;\n    }\n}\n"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	minor, err := ctrl.resources.nextGlobalMinor(context.Background(), []string{"10.0.0.1"})
	require.NoError(t, err)
	assert.Equal(t, 1000, minor)
}
