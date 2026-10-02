package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	v1 "github.com/haify-project/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Gateway edits read the promoter config from the nodes that hold it, never
// from the controller's own filesystem. These tests run with a mock deployment
// whose nodes hold files the controller does not.

var scriptCmdRE = regexp.MustCompile(`^echo (\S+) \| base64 -d \| sudo /bin/sh$`)
var dumpLoopRE = regexp.MustCompile(`for f in ([^;]+); do`)

// dumpScriptPatterns returns the file patterns a configDumpScript command
// reads, or nil when cmd is something else.
func dumpScriptPatterns(cmd string) []string {
	m := scriptCmdRE.FindStringSubmatch(cmd)
	if m == nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil || !strings.Contains(string(raw), configDumpMarker) {
		return nil
	}
	loop := dumpLoopRE.FindStringSubmatch(string(raw))
	if loop == nil {
		return nil
	}
	return strings.Fields(loop[1])
}

// dumpConfigs answers a dump script the way the nodes would.
func (m *MockDeploymentClient) dumpConfigs(hosts, patterns []string) map[string]string {
	out := map[string]string{}
	for _, h := range hosts {
		files := m.Configs
		if m.NodeConfigs != nil {
			var answered bool
			if files, answered = m.NodeConfigs[h]; !answered {
				continue
			}
		}
		var b strings.Builder
		b.WriteString("Last login: noise the parser must skip\n")
		for _, path := range sortedKeys(files) {
			for _, p := range patterns {
				if ok, _ := filepath.Match(p, path); ok {
					fmt.Fprintf(&b, "%s %s %s\n", configDumpMarker, path,
						base64.StdEncoding.EncodeToString([]byte(files[path])))
					break
				}
			}
		}
		out[h] = b.String()
	}
	return out
}

func testISCSIConfig(t *testing.T, iqn string) string {
	t.Helper()
	i := NewISCSIManager(New(nil, &MockDeploymentClient{}, zap.NewNop(), nil))
	sip, err := parseServiceIP("192.168.1.200/24")
	require.NoError(t, err)
	cfg, err := i.generateISCSIGatewayConfig(&v1.CreateISCSIGatewayRequest{
		Resource: "blk", Iqn: iqn, ServiceIp: "192.168.1.200/24",
	}, sip, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	return cfg
}

// blkResources says resource blk has its diskful replicas on node2 and node3.
func blkResources() *MockResourceManager {
	return &MockResourceManager{Resources: map[string]*ResourceInfo{
		"blk": {Name: "blk", Nodes: []string{"node2", "node3"}, Hosts: []string{"node2", "node3"}},
	}}
}

const blkPath = DrbdReactorConfigDir + "/sds-iscsi-blk.toml"

// The controller (node1, or no node at all) holds no copy; the replicas do.
func TestGatewayEditReadsTheResourceNodes(t *testing.T) {
	cfg := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	dep := &MockDeploymentClient{NodeConfigs: map[string]map[string]string{
		"node1": {},
		"node2": {blkPath: cfg},
		"node3": {blkPath: cfg},
	}}
	iscsi := NewISCSIManager(New(blkResources(), dep, zap.NewNop(), []string{"node1", "node2", "node3"}))
	_, err := os.Stat(blkPath)
	require.True(t, os.IsNotExist(err), "the test machine must not hold the config itself")

	luns, err := iscsi.ListLUNs(context.Background(), "blk")
	require.NoError(t, err)
	assert.Len(t, luns, 1)
	assert.Equal(t, []string{"node2", "node3"}, dep.ExecHosts[0], "read from the diskful nodes only")
	assert.Contains(t, dep.ExecCommands[0], "| base64 -d | sudo /bin/sh", "shell goes base64-wrapped")

	require.NoError(t, iscsi.AddLUN(context.Background(), "blk", 5, "/dev/drbd9"))
	assert.Equal(t, []string{"node2", "node3"}, dep.ConfigHosts[blkPath], "written to every diskful node")
	assert.Contains(t, dep.Configs[blkPath], "lun=5")
	assert.Contains(t, dep.Configs[blkPath], "iqn.2024-01.com.example:blk", "edited the nodes' copy")
}

func TestGatewayEditNotFoundOnNodes(t *testing.T) {
	dep := &MockDeploymentClient{NodeConfigs: map[string]map[string]string{"node2": {}, "node3": {}}}
	iscsi := NewISCSIManager(New(blkResources(), dep, zap.NewNop(), []string{"node1", "node2", "node3"}))
	_, err := iscsi.ListLUNs(context.Background(), "blk")
	assert.ErrorContains(t, err, "not found on node2,node3")

	dep = &MockDeploymentClient{NodeConfigs: map[string]map[string]string{}}
	iscsi = NewISCSIManager(New(blkResources(), dep, zap.NewNop(), []string{"node2", "node3"}))
	_, err = iscsi.ListLUNs(context.Background(), "blk")
	assert.ErrorContains(t, err, "no node answered")

	_, err = NewISCSIManager(New(blkResources(), execOnlyDeployment{}, zap.NewNop(), []string{"node2"})).
		ListLUNs(context.Background(), "blk")
	assert.ErrorContains(t, err, "cannot read node output")
}

// A live .toml anywhere beats a .toml.disabled copy, even a newer-looking one.
func TestGatewayEditPrefersLiveConfig(t *testing.T) {
	live := testISCSIConfig(t, "iqn.2024-01.com.example:live")
	stale := testISCSIConfig(t, "iqn.2024-01.com.example:stale")
	dep := &MockDeploymentClient{NodeConfigs: map[string]map[string]string{
		"node2": {blkPath + ".disabled": stale},
		"node3": {blkPath: live, blkPath + ".disabled": stale},
	}}
	m := New(blkResources(), dep, zap.NewNop(), []string{"node2", "node3"})

	cfg, err := m.readGatewayConfig(context.Background(), "blk", "sds-iscsi-blk")
	require.NoError(t, err)
	assert.False(t, cfg.disabled)
	assert.Equal(t, live, cfg.content)
}

// A stopped gateway is edited in its .toml.disabled copy: writing the live
// .toml would start it.
func TestGatewayEditOfStoppedGatewayStaysStopped(t *testing.T) {
	cfg := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	dep := &MockDeploymentClient{NodeConfigs: map[string]map[string]string{
		"node2": {blkPath + ".disabled": cfg},
		"node3": {blkPath + ".disabled": cfg},
	}}
	iscsi := NewISCSIManager(New(blkResources(), dep, zap.NewNop(), []string{"node1", "node2", "node3"}))

	require.NoError(t, iscsi.AddInitiator(context.Background(), "blk", "iqn.2024-01.com.example:init"))
	_, wroteLive := dep.Configs[blkPath]
	assert.False(t, wroteLive)
	assert.Contains(t, dep.Configs[blkPath+".disabled"], "iqn.2024-01.com.example:init")
	assert.Equal(t, []string{"node2", "node3"}, dep.ConfigHosts[blkPath+".disabled"])
	for _, cmd := range dep.ExecCommands {
		assert.NotContains(t, cmd, "systemctl reload drbd-reactor", "a stopped gateway is not reloaded")
	}
}

// Nodes that disagree: the copy most nodes hold wins, a tie goes to the first
// node by name, and the warning names the nodes that hold something else.
func TestGatewayEditNodesDisagree(t *testing.T) {
	a := testISCSIConfig(t, "iqn.2024-01.com.example:a")
	b := testISCSIConfig(t, "iqn.2024-01.com.example:b")
	core, logs := observer.New(zapcore.WarnLevel)
	dep := &MockDeploymentClient{NodeConfigs: map[string]map[string]string{
		"node1": {blkPath: a},
		"node2": {blkPath: b},
		"node3": {blkPath: b},
		"node4": {},
	}}
	res := &MockResourceManager{Resources: map[string]*ResourceInfo{
		"blk": {Name: "blk", Hosts: []string{"node3", "node1", "node2", "node4"}},
	}}
	m := New(res, dep, zap.New(core), []string{"node1", "node2", "node3", "node4"})

	cfg, err := m.readGatewayConfig(context.Background(), "blk", "sds-iscsi-blk")
	require.NoError(t, err)
	assert.Equal(t, b, cfg.content, "majority wins")

	require.Equal(t, 1, logs.Len())
	fields := logs.All()[0].ContextMap()
	assert.Equal(t, []interface{}{"node2", "node3"}, fields["used_copy_from"])
	assert.Equal(t, []interface{}{"node1"}, fields["differing_nodes"])
	assert.Equal(t, []interface{}{"node4"}, fields["missing_on"])

	// Tie: the first node by name, whatever order the hosts come in.
	got, from, differing := chooseConfigCopy([]nodeConfigCopy{
		{host: "node1", content: a}, {host: "node2", content: b},
		{host: "node3", content: b}, {host: "node4", content: a},
	})
	assert.Equal(t, a, got.content)
	assert.Equal(t, []string{"node1", "node4"}, from)
	assert.Equal(t, []string{"node2", "node3"}, differing)

	// Identical copies: no warning.
	logs.TakeAll()
	dep.NodeConfigs = map[string]map[string]string{"node1": {blkPath: a}, "node2": {blkPath: a}, "node3": {blkPath: a}, "node4": {blkPath: a}}
	_, err = m.readGatewayConfig(context.Background(), "blk", "sds-iscsi-blk")
	require.NoError(t, err)
	assert.Zero(t, logs.Len())
}

// ListTargets is the union of the running targets on every managed node.
func TestListTargetsReadsTheNodes(t *testing.T) {
	dep := &MockDeploymentClient{NodeConfigs: map[string]map[string]string{
		"node1": {DrbdReactorConfigDir + "/sds-iscsi-a.toml": testISCSIConfig(t, "iqn.2024-01.com.example:a")},
		"node2": {
			DrbdReactorConfigDir + "/sds-iscsi-a.toml":          testISCSIConfig(t, "iqn.2024-01.com.example:a"),
			DrbdReactorConfigDir + "/sds-iscsi-b.toml":          testISCSIConfig(t, "iqn.2024-01.com.example:b"),
			DrbdReactorConfigDir + "/sds-iscsi-c.toml.disabled": testISCSIConfig(t, "iqn.2024-01.com.example:c"),
		},
	}}
	iscsi := NewISCSIManager(New(nil, dep, zap.NewNop(), []string{"node1", "node2", "node3"}))

	targets, err := iscsi.ListTargets(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, []string{"iqn.2024-01.com.example:a", "iqn.2024-01.com.example:b"}, targets)
	assert.Equal(t, []string{"node1", "node2", "node3"}, dep.ExecHosts[0])

	_, err = NewISCSIManager(New(nil, dep, zap.NewNop(), nil)).ListTargets(context.Background(), "")
	assert.ErrorContains(t, err, "no nodes")
}

func TestListSubsystemsReadsTheNodes(t *testing.T) {
	gen := NewNVMeManager(New(nil, &MockDeploymentClient{}, zap.NewNop(), nil))
	sip, err := parseServiceIP("192.168.1.150/24")
	require.NoError(t, err)
	cfg, err := gen.generateNVMeGatewayConfig(&v1.CreateNVMeGatewayRequest{
		Resource: "x", Nqn: "nqn.2024-01.com.example:x", ServiceIp: "192.168.1.150/24",
	}, sip, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	dep := &MockDeploymentClient{NodeConfigs: map[string]map[string]string{
		"node2": {DrbdReactorConfigDir + "/sds-nvmeof-x.toml": cfg},
	}}
	nvme := NewNVMeManager(New(nil, dep, zap.NewNop(), []string{"node1", "node2"}))
	got, err := nvme.ListSubsystems(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, []string{"nqn.2024-01.com.example:x"}, got)
}

func TestParseConfigDump(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString
	files, err := parseConfigDump("sudo: noise\n" +
		configDumpMarker + " /x/a.toml " + enc([]byte("a\nb\n")) + "\n" +
		configDumpMarker + " /x/empty.toml \n")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"/x/a.toml": "a\nb\n", "/x/empty.toml": ""}, files)

	_, err = parseConfigDump(configDumpMarker + " /x/a.toml !!notbase64")
	assert.Error(t, err)
}

// The dump script itself, run by a real shell: byte-exact content, trailing
// newline kept, unmatched patterns and absent files skipped.
func TestConfigDumpScriptRuns(t *testing.T) {
	if _, err := exec.LookPath("base64"); err != nil {
		t.Skip("no base64 on this machine")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sds-iscsi-a.toml"), []byte("x = \"$HOME\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sds-iscsi-b.toml.disabled"), []byte(""), 0o644))

	script := configDumpScript(filepath.Join(dir, "sds-iscsi-*.toml"), filepath.Join(dir, "sds-iscsi-b.toml.disabled"),
		filepath.Join(dir, "missing.toml"))
	out, err := exec.Command("/bin/sh", "-c", script).Output()
	require.NoError(t, err)
	files, err := parseConfigDump(string(out))
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(dir, "sds-iscsi-a.toml"), filepath.Join(dir, "sds-iscsi-b.toml.disabled")}, sortedKeys(files))
	assert.Equal(t, "x = \"$HOME\"\n", files[filepath.Join(dir, "sds-iscsi-a.toml")])
}
