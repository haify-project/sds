package gateway

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	v1 "github.com/haify-project/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// diskfulResources is a resource with two diskful replicas and a tiebreaker:
// Nodes lists all three, Hosts only the replicas.
func diskfulResources(name string) *MockResourceManager {
	return &MockResourceManager{Resources: map[string]*ResourceInfo{
		name: {
			Name:    name,
			Nodes:   []string{"node1", "node2", "tb"},
			Hosts:   []string{"node1", "node2"},
			Role:    "Primary",
			Volumes: testVolumes(2),
		},
	}}
}

// scriptRun is one runScript invocation as the mock recorded it.
type scriptRun struct {
	script string
	hosts  []string
}

// scriptsRun decodes every runScript-style command the mock recorded.
func scriptsRun(t *testing.T, dep *MockDeploymentClient) []scriptRun {
	t.Helper()
	var runs []scriptRun
	for i, cmd := range dep.ExecCommands {
		if strings.Contains(cmd, "base64 -d") {
			runs = append(runs, scriptRun{script: decodeScriptCommand(t, cmd), hosts: dep.ExecHosts[i]})
		}
	}
	return runs
}

// findScript returns the first decoded script containing marker.
func findScript(t *testing.T, dep *MockDeploymentClient, marker string) scriptRun {
	t.Helper()
	for _, r := range scriptsRun(t, dep) {
		if strings.Contains(r.script, marker) {
			return r
		}
	}
	t.Fatalf("no script containing %q was run", marker)
	return scriptRun{}
}

// ==================== The probe itself ====================

// The probe must actually detect what is missing when a shell runs it. Sent
// as a plain command, dispatch's quoting emptied $missing and the check
// passed on every node; this runs the script the way runScript delivers it.
func TestPrereqScriptReportsMissingItems(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	script := prereqScript(gatewayPrereqs{
		agents: []string{"sds-no-such-agent"},
		tools:  []string{"sh", "sds-no-such-tool"},
	})
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	require.Error(t, err, "a missing agent and tool must fail the probe")
	assert.Contains(t, string(out), "ocf:heartbeat:sds-no-such-agent")
	assert.Contains(t, string(out), "sds-no-such-tool")
	assert.NotContains(t, string(out), " sh ", "an installed tool is not reported")

	out, err = exec.Command("sh", "-c", prereqScript(gatewayPrereqs{tools: []string{"sh"}})).CombinedOutput()
	assert.NoError(t, err, string(out))
}

func TestCheckGatewayPrereqsGoesThroughRunScript(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(nil, dep, zap.NewNop(), nil)
	require.NoError(t, m.checkGatewayPrereqs(context.Background(), []string{"node1"}, nfsPrereqs()))
	require.Len(t, dep.ExecCommands, 1)
	assert.Contains(t, dep.ExecCommands[0], "base64 -d", "$missing must survive dispatch's quoting")
}

// ==================== NFS server ====================

func TestCreateNFSGatewayChecksNFSServerOnDiskfulNodes(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(diskfulResources("data"), dep, zap.NewNop(), []string{"node1", "node2", "tb"})

	resp, err := NewNFSManager(m).CreateNFSGateway(context.Background(), &v1.CreateNFSGatewayRequest{
		Resource: "data", ServiceIp: "192.168.1.100/24", ExportPath: "/data",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)

	probe := findScript(t, dep, "missing=")
	assert.Equal(t, []string{"node1", "node2"}, probe.hosts, "the tiebreaker never runs the chain")
	for _, item := range []string{"ocf:heartbeat:nfsserver", "ocf:heartbeat:exportfs", "rpc.nfsd", "missing=\"$missing exportfs\""} {
		assert.Contains(t, probe.script, item)
	}
}

func TestCreateNFSGatewayMissingNFSServerSaysWhatToInstall(t *testing.T) {
	dep := &MockDeploymentClient{ExecErr: fmt.Errorf("command failed on node1: missing: rpc.nfsd exportfs")}
	m := New(diskfulResources("data"), dep, zap.NewNop(), []string{"node1", "node2"})

	resp, err := NewNFSManager(m).CreateNFSGateway(context.Background(), &v1.CreateNFSGatewayRequest{
		Resource: "data", ServiceIp: "192.168.1.100/24", ExportPath: "/data",
	})
	require.Error(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "nfs-kernel-server (Debian/Ubuntu) or nfs-utils (EL)")
	assert.Contains(t, resp.Message, "missing: rpc.nfsd exportfs")
}

// ==================== iSCSI implementation ====================

func TestValidateISCSIImplementation(t *testing.T) {
	for _, in := range []string{"", "lio", "lio-t"} {
		got, err := validateISCSIImplementation(in)
		require.NoError(t, err, in)
		assert.Equal(t, "lio-t", got, in)
	}
	for in, reason := range map[string]string{
		"tgt":  "tgtd",
		"iet":  "not packaged",
		"scst": "unknown",
	} {
		_, err := validateISCSIImplementation(in)
		require.Error(t, err, in)
		assert.Contains(t, err.Error(), reason)
		assert.Contains(t, err.Error(), "--implementation lio")
	}
}

// tgt and iet are refused before anything touches a node, rather than
// checked for targetcli and then written into a config they cannot run.
func TestCreateISCSIGatewayRefusesTgtAndIetBeforeAnySideEffect(t *testing.T) {
	for _, impl := range []string{"tgt", "iet"} {
		dep := &MockDeploymentClient{}
		m := New(diskfulResources("blk"), dep, zap.NewNop(), []string{"node1", "node2"})
		resp, err := NewISCSIManager(m).CreateISCSIGateway(context.Background(), &v1.CreateISCSIGatewayRequest{
			Resource: "blk", Iqn: "iqn.2024-01.com.example:sds.blk", ServiceIp: "192.168.1.101/24",
			Implementation: impl,
		})
		require.Error(t, err, impl)
		assert.False(t, resp.Success)
		assert.Contains(t, resp.Message, "not supported")
		assert.Empty(t, dep.ExecCommands, impl)
		assert.Empty(t, dep.Configs, impl)
	}
}

func TestCreateISCSIGatewayChecksTargetcliAndPinsImplementation(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(diskfulResources("blk"), dep, zap.NewNop(), []string{"node1", "node2", "tb"})
	resp, err := NewISCSIManager(m).CreateISCSIGateway(context.Background(), &v1.CreateISCSIGatewayRequest{
		Resource: "blk", Iqn: "iqn.2024-01.com.example:sds.blk", ServiceIp: "192.168.1.101/24",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)

	probe := findScript(t, dep, "missing=")
	assert.Equal(t, []string{"node1", "node2"}, probe.hosts)
	assert.Contains(t, probe.script, "targetcli")
	assert.NotContains(t, probe.script, "tgtadm")

	// The LU agent must use the target's implementation, not its own guess.
	config := dep.Configs["/etc/drbd-reactor.d/sds-iscsi-blk.toml"]
	for _, line := range strings.Split(config, "\n") {
		if strings.Contains(line, "iSCSILogicalUnit") {
			assert.Contains(t, line, "implementation=lio-t")
		}
	}
	assert.Contains(t, buildISCSILUNLine(2, "iqn.x:r", "/dev/drbd9", "lio-t"), "implementation=lio-t\",")
}

// ==================== NVMe-oF transport modules ====================

func TestNVMeTransportModule(t *testing.T) {
	for transport, want := range map[string]string{"": "nvmet-tcp", "tcp": "nvmet-tcp", "rdma": "nvmet-rdma"} {
		got, err := nvmeTransportModule(transport)
		require.NoError(t, err)
		assert.Equal(t, want, got, transport)
	}
	_, err := nvmeTransportModule("fc")
	assert.Error(t, err)
}

func TestEnsureNVMeModulesLoadsAndPersistsTheTransportModule(t *testing.T) {
	dep := &MockDeploymentClient{}
	nvme := NewNVMeManager(New(nil, dep, zap.NewNop(), nil))

	require.NoError(t, nvme.ensureNVMeModules(context.Background(), []string{"node1"}, "rdma"))
	script := decodeScriptCommand(t, dep.ExecCommands[0])
	assert.Contains(t, script, "modprobe nvmet-rdma")
	assert.NotContains(t, script, "nvmet-tcp")
	assert.Contains(t, script, "for m in nvmet nvmet-rdma")
	assert.Contains(t, script, "/sys/class/infiniband", "rdma needs an RDMA device")
	assert.NotContains(t, script, "> /etc/modules-load.d", "persisting must add to the file, not replace it")

	require.NoError(t, nvme.ensureNVMeModules(context.Background(), []string{"node1"}, "tcp"))
	script = decodeScriptCommand(t, dep.ExecCommands[1])
	assert.Contains(t, script, "modprobe nvmet-tcp")
	assert.NotContains(t, script, "rdma")
	assert.NotContains(t, script, "infiniband")
}

func TestCreateNVMeGatewayRDMALoadsNvmetRdmaOnDiskfulNodes(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(diskfulResources("fast"), dep, zap.NewNop(), []string{"node1", "node2", "tb"})
	resp, err := NewNVMeManager(m).CreateNVMeGateway(context.Background(), &v1.CreateNVMeGatewayRequest{
		Resource: "fast", Nqn: "nqn.2024-01.com.example:sds.fast", ServiceIp: "192.168.1.102/24",
		TransportType: "rdma",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)

	mods := findScript(t, dep, "modprobe nvmet\n")
	assert.Equal(t, []string{"node1", "node2"}, mods.hosts)
	assert.Contains(t, mods.script, "modprobe nvmet-rdma")
	assert.Contains(t, dep.Configs["/etc/drbd-reactor.d/sds-nvmeof-fast.toml"], "type=rdma")
}

func TestCreateNVMeGatewayRefusesFC(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(diskfulResources("fast"), dep, zap.NewNop(), []string{"node1", "node2"})
	_, err := NewNVMeManager(m).CreateNVMeGateway(context.Background(), &v1.CreateNVMeGatewayRequest{
		Resource: "fast", Nqn: "nqn.2024-01.com.example:sds.fast", ServiceIp: "192.168.1.102/24",
		TransportType: "fc",
	})
	require.Error(t, err)
	assert.Empty(t, dep.ExecCommands)
}
