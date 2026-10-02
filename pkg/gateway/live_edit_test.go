package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Editing a running gateway must not reload drbd-reactor where the gateway
// runs (a changed promoter is stopped and restarted, and with it the whole
// service chain), must reach the running target there, and must leave a
// stopped gateway entirely alone.

// liveCluster is resource blk on node2 and node3, running on node2; node1 is
// another managed host.
func liveCluster(t *testing.T, config, pluginID string) (*Manager, *MockDeploymentClient) {
	t.Helper()
	dep := &MockDeploymentClient{TargetStates: map[string]string{"node2": "active", "node3": "inactive"}}
	dep.SetConfig(gatewayConfigPath(pluginID), config)
	return New(blkResources(), dep, zap.NewNop(), []string{"node1", "node2", "node3"}), dep
}

// sent is one command a test saw, with the script decoded when it is one.
type sent struct {
	hosts  []string
	cmd    string
	script string
}

func sentCommands(dep *MockDeploymentClient) []sent {
	var out []sent
	for i, cmd := range dep.ExecCommands {
		out = append(out, sent{hosts: dep.ExecHosts[i], cmd: cmd, script: decodeScriptCmd(cmd)})
	}
	return out
}

// onHost returns the text of every command that ran on host.
func onHost(dep *MockDeploymentClient, host string) string {
	var b strings.Builder
	for _, s := range sentCommands(dep) {
		for _, h := range s.hosts {
			if h == host {
				b.WriteString(s.cmd + "\n" + s.script + "\n")
			}
		}
	}
	return b.String()
}

func assertNoReloadOn(t *testing.T, dep *MockDeploymentClient, host string) {
	t.Helper()
	text := onHost(dep, host)
	assert.NotContains(t, text, "reload drbd-reactor", "drbd-reactor must not be reloaded on %s", host)
	assert.NotContains(t, text, "restart drbd-reactor", "drbd-reactor must not be restarted on %s", host)
}

func TestEditRunningISCSIInitiatorAppliesOnPrimaryOnly(t *testing.T) {
	cfg := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	m, dep := liveCluster(t, cfg, "sds-iscsi-blk")
	iscsi := NewISCSIManager(m)

	require.NoError(t, iscsi.AddInitiator(context.Background(), "blk", "iqn.2024-01.com.example:host-b"))

	assert.Equal(t, []string{"node2"}, dep.ConfigHosts[blkPath+pendingSuffix], "the running node gets the pending copy")
	assert.Equal(t, []string{"node3"}, dep.ConfigHosts[blkPath], "the other replica gets the live config")
	assertNoReloadOn(t, dep, "node2")
	assert.Contains(t, onHost(dep, "node3"), reloadReactorCmd, "the idle replica reloads to regenerate its units")

	primary := onHost(dep, "node2")
	assert.Contains(t, primary, `targetcli "$t/acls" create "$i" add_mapped_luns=true`)
	assert.Contains(t, primary, "want='iqn.2024-01.com.example:host-b'")
	assert.Contains(t, primary, "50-sds-pending-gateway-config.conf", "the pending-config hook is installed")
	assert.Contains(t, primary, "systemctl daemon-reload")
	assert.NotContains(t, primary, "systemctl start 'ocf.rs@", "no unit is started for an ACL change")
	assert.NotContains(t, primary, "systemctl stop 'ocf.rs@", "no unit is stopped for an ACL change")

	for _, s := range sentCommands(dep) {
		if strings.Contains(s.script, "targetcli") {
			assert.Equal(t, []string{"node2"}, s.hosts, "live commands go to the running node only")
		}
	}
	assert.NotContains(t, onHost(dep, "node3"), "targetcli")
	assert.NotContains(t, onHost(dep, "node1"), "targetcli")
}

func TestEditStoppedGatewayTouchesNothingLive(t *testing.T) {
	cfg := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	dep := &MockDeploymentClient{TargetStates: map[string]string{"node2": "active", "node3": "inactive"}}
	dep.SetConfig(blkPath+disabledSuffix, cfg)
	iscsi := NewISCSIManager(New(blkResources(), dep, zap.NewNop(), []string{"node1", "node2", "node3"}))

	require.NoError(t, iscsi.AddInitiator(context.Background(), "blk", "iqn.2024-01.com.example:host-b"))
	require.NoError(t, iscsi.AddLUN(context.Background(), "blk", 2, "/dev/drbd5"))

	assert.Equal(t, []string{"node2", "node3"}, dep.ConfigHosts[blkPath+disabledSuffix])
	_, pending := dep.ConfigHosts[blkPath+pendingSuffix]
	assert.False(t, pending)
	for _, s := range sentCommands(dep) {
		text := s.cmd + s.script
		for _, live := range []string{"targetcli", "systemctl", targetStateMarker, "drbd-reactor.service.d"} {
			assert.NotContains(t, text, live, "a stopped gateway gets no live command")
		}
	}
}

func TestEditRunningGatewayNowhereWritesAndReloads(t *testing.T) {
	cfg := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	m, dep := liveCluster(t, cfg, "sds-iscsi-blk")
	dep.TargetStates = map[string]string{"node2": "inactive", "node3": "failed"}

	require.NoError(t, NewISCSIManager(m).AddInitiator(context.Background(), "blk", "iqn.2024-01.com.example:host-b"))
	assert.Equal(t, []string{"node2", "node3"}, dep.ConfigHosts[blkPath])
	assert.NotContains(t, onHost(dep, "node2"), "targetcli")
}

func TestEditRunningGatewayRefusedWhileSettling(t *testing.T) {
	for name, states := range map[string]map[string]string{
		"activating": {"node2": "activating", "node3": "inactive"},
		"no answer":  {"node2": "active"},
		"two active": {"node2": "active", "node3": "active"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
			m, dep := liveCluster(t, cfg, "sds-iscsi-blk")
			dep.TargetStates = states
			err := NewISCSIManager(m).AddInitiator(context.Background(), "blk", "iqn.2024-01.com.example:host-b")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "nothing was changed")
			assert.Empty(t, dep.ConfigHosts, "no config written")
		})
	}
}

func TestEditRunningGatewayLiveFailureIsReported(t *testing.T) {
	cfg := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	m, dep := liveCluster(t, cfg, "sds-iscsi-blk")
	dep.ScriptErr = map[string]error{"add_mapped_luns": errors.New("exit status 1")}

	err := NewISCSIManager(m).AddInitiator(context.Background(), "blk", "iqn.2024-01.com.example:host-b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "saved on every node")
	assert.Contains(t, err.Error(), "node2")
	assert.Contains(t, err.Error(), "sds gateway stop blk")
}

func TestEditRunningISCSILUNStartsAndStopsOnlyThatUnit(t *testing.T) {
	cfg := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	m, dep := liveCluster(t, cfg, "sds-iscsi-blk")
	iscsi := NewISCSIManager(m)

	require.NoError(t, iscsi.AddLUN(context.Background(), "blk", 2, "/dev/drbd5"))
	primary := onHost(dep, "node2")
	assert.Contains(t, primary, "systemctl start 'ocf.rs@lu2_blk.service'")
	assert.NotContains(t, primary, "systemctl stop")
	assertNoReloadOn(t, dep, "node2")

	dep.ExecCommands, dep.ExecHosts = nil, nil
	require.NoError(t, iscsi.RemoveLUN(context.Background(), "blk", 2))
	primary = onHost(dep, "node2")
	assert.Contains(t, primary, "systemctl stop 'ocf.rs@lu2_blk.service'")
	assert.NotContains(t, primary, "systemctl start 'ocf.rs@")
	assert.NotContains(t, onHost(dep, "node3"), "systemctl stop 'ocf.rs@")
	assertNoReloadOn(t, dep, "node2")
}

func TestEditRunningNVMeHostsAndNamespaces(t *testing.T) {
	nvme := NewNVMeManager(New(nil, &MockDeploymentClient{}, zap.NewNop(), nil))
	sip, err := parseServiceIP("192.168.1.150/24")
	require.NoError(t, err)
	cfg, err := nvme.generateNVMeGatewayConfig(&v1.CreateNVMeGatewayRequest{
		Resource: "blk", Nqn: "nqn.2024-01.com.example:blk", ServiceIp: "192.168.1.150/24",
	}, sip, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	m, dep := liveCluster(t, cfg, "sds-nvmeof-blk")
	nvme = NewNVMeManager(m)

	require.NoError(t, nvme.AddHost(context.Background(), "blk", "nqn.2014-08.org.nvmexpress:uuid:3c1e4a0e-8f2b-4c6d-9e1a-2b3c4d5e6f70"))
	primary := onHost(dep, "node2")
	assert.Contains(t, primary, `ln -s "$h/$n" "$s/allowed_hosts/$n"`)
	assert.Contains(t, primary, `echo 0 > "$s/attr_allow_any_host"`)
	assertNoReloadOn(t, dep, "node2")

	dep.ExecCommands, dep.ExecHosts = nil, nil
	require.NoError(t, nvme.AddNamespace(context.Background(), "blk", "/dev/drbd7"))
	assert.Contains(t, onHost(dep, "node2"), "systemctl start 'ocf.rs@ns_2_blk.service'")
	assertNoReloadOn(t, dep, "node2")
}

func TestEditRunningNFSExportStartsItsUnit(t *testing.T) {
	nfs := NewNFSManager(New(nil, &MockDeploymentClient{}, zap.NewNop(), nil))
	sip, err := parseServiceIP("192.168.1.200/24")
	require.NoError(t, err)
	cfg, err := nfs.generateNFSGatewayConfig(&v1.CreateNFSGatewayRequest{
		Resource: "blk", ServiceIp: "192.168.1.200/24", ExportPath: "/srv/blk",
	}, sip, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	m, dep := liveCluster(t, cfg, "sds-nfs-blk")
	nfs = NewNFSManager(m)

	require.NoError(t, nfs.AddNFSExport(context.Background(), "blk", "/srv/blk/sub", 0, "10.0.0.0/8", ""))
	primary := onHost(dep, "node2")
	assert.Regexp(t, `systemctl start 'ocf\.rs@export_\d+_0_blk\.service'`, primary)
	assertNoReloadOn(t, dep, "node2")
}

func TestPendingConfigIsTheRunningNodesCopy(t *testing.T) {
	old := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	edited := strings.Replace(old, "allowed_initiators=", "allowed_initiators=iqn.2024-01.com.example:x", 1)
	dep := &MockDeploymentClient{NodeConfigs: map[string]map[string]string{
		"node2": {blkPath: old, blkPath + pendingSuffix: edited},
		"node3": {blkPath: edited},
	}}
	m := New(blkResources(), dep, zap.NewNop(), []string{"node2", "node3"})
	cfg, err := m.readGatewayConfig(context.Background(), "blk", "sds-iscsi-blk")
	require.NoError(t, err)
	assert.Equal(t, edited, cfg.content)
	assert.False(t, cfg.disabled)
}

func TestStopGatewayTurnsPendingIntoDisabled(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(nil, dep, zap.NewNop(), []string{"node1"})
	require.NoError(t, m.StopGateway(context.Background(), "blk"))
	assert.Contains(t, decodeScriptCmd(dep.ExecCommands[0]), `mv -f "$f.pending" "$f.disabled"`)
}
