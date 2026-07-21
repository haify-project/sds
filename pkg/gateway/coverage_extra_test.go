package gateway

import (
	"context"
	"fmt"
	"testing"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// covResources builds a MockResourceManager holding a single resource with the
// given number of volumes and one node, which is enough to drive the full
// gateway-create success path (prereq check + promote + mkfs + config write).
func covResources(name string, volumeCount int) *MockResourceManager {
	vols := make([]*ResourceVolumeInfo, volumeCount)
	for i := 0; i < volumeCount; i++ {
		vols[i] = &ResourceVolumeInfo{VolumeID: uint32(i), Device: fmt.Sprintf("/dev/drbd%d", i), SizeGB: 1}
	}
	return &MockResourceManager{
		Resources: map[string]*ResourceInfo{
			name: {Name: name, Nodes: []string{"node1"}, Role: "Primary", Volumes: vols},
		},
	}
}

// ==================== Create*Gateway success paths ====================

func TestCovCreateNFSGatewaySuccess(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(covResources("data", 2), dep, zap.NewNop(), []string{"node1"})
	nfs := NewNFSManager(m)

	resp, err := nfs.CreateNFSGateway(context.Background(), &v1.CreateNFSGatewayRequest{
		Resource:   "data",
		ServiceIp:  "192.168.1.100/24",
		ExportPath: "/srv/exp",
		AllowedIps: []string{"10.0.0.0/8"},
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
	assert.Contains(t, resp.ConfigPath, "sds-nfs-data.toml")

	// The generated config was distributed to the node.
	cfg, ok := dep.GetConfig(gatewayConfigPath("sds-nfs-data"))
	require.True(t, ok)
	assert.Contains(t, cfg, "ocf:heartbeat:exportfs")
	assert.Contains(t, cfg, "ocf:heartbeat:nfsserver")
}

func TestCovCreateISCSIGatewaySuccess(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(covResources("data", 2), dep, zap.NewNop(), []string{"node1"})
	iscsi := NewISCSIManager(m)

	resp, err := iscsi.CreateISCSIGateway(context.Background(), &v1.CreateISCSIGatewayRequest{
		Resource:  "data",
		Iqn:       "iqn.2024-01.com.example:sds.data",
		ServiceIp: "192.168.1.101/24",
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
	assert.Contains(t, resp.ConfigPath, "sds-iscsi-data.toml")

	cfg, ok := dep.GetConfig(gatewayConfigPath("sds-iscsi-data"))
	require.True(t, ok)
	assert.Contains(t, cfg, "ocf:heartbeat:iSCSITarget")
	assert.Contains(t, cfg, "ocf:heartbeat:iSCSILogicalUnit")
}

func TestCovCreateNVMeGatewaySuccess(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(covResources("data", 2), dep, zap.NewNop(), []string{"node1"})
	nvme := NewNVMeManager(m)

	resp, err := nvme.CreateNVMeGateway(context.Background(), &v1.CreateNVMeGatewayRequest{
		Resource:  "data",
		Nqn:       "nqn.2024-01.com.example:sds.data",
		ServiceIp: "192.168.1.102/24",
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
	assert.Contains(t, resp.ConfigPath, "sds-nvmeof-data.toml")

	cfg, ok := dep.GetConfig(gatewayConfigPath("sds-nvmeof-data"))
	require.True(t, ok)
	assert.Contains(t, cfg, "ocf:heartbeat:nvmet-subsystem")
	assert.Contains(t, cfg, "ocf:heartbeat:nvmet-port")
}

// ==================== Create*Gateway error paths ====================

func TestCovCreateGatewayInvalidServiceIP(t *testing.T) {
	m := New(covResources("data", 2), &MockDeploymentClient{}, zap.NewNop(), []string{"node1"})

	nfsResp, err := NewNFSManager(m).CreateNFSGateway(context.Background(), &v1.CreateNFSGatewayRequest{
		Resource: "data", ServiceIp: "not-a-cidr",
	})
	assert.Error(t, err)
	assert.False(t, nfsResp.Success)
	assert.Contains(t, nfsResp.Message, "invalid service IP")

	iscsiResp, err := NewISCSIManager(m).CreateISCSIGateway(context.Background(), &v1.CreateISCSIGatewayRequest{
		Resource: "data", ServiceIp: "bad",
	})
	assert.Error(t, err)
	assert.False(t, iscsiResp.Success)

	nvmeResp, err := NewNVMeManager(m).CreateNVMeGateway(context.Background(), &v1.CreateNVMeGatewayRequest{
		Resource: "data", ServiceIp: "bad",
	})
	assert.Error(t, err)
	assert.False(t, nvmeResp.Success)
}

// checkGatewayPrereqs fails when the Exec that probes for OCF agents errors,
// which short-circuits create with a clear message.
func TestCovCreateGatewayPrereqCheckFails(t *testing.T) {
	dep := &MockDeploymentClient{ExecErr: fmt.Errorf("agent missing")}
	m := New(covResources("data", 2), dep, zap.NewNop(), []string{"node1"})

	resp, err := NewNFSManager(m).CreateNFSGateway(context.Background(), &v1.CreateNFSGatewayRequest{
		Resource: "data", ServiceIp: "192.168.1.100/24",
	})
	assert.Error(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "prerequisites missing")
}

// ensureGatewayPrerequisites fails when promotion (SetPrimary) errors.
func TestCovCreateGatewayPromoteFails(t *testing.T) {
	res := covResources("data", 2)
	res.SetPrimaryFunc = func(ctx context.Context, resource, node string, force bool) error {
		return fmt.Errorf("promote boom")
	}
	dep := &MockDeploymentClient{}
	m := New(res, dep, zap.NewNop(), []string{"node1"})

	resp, err := NewISCSIManager(m).CreateISCSIGateway(context.Background(), &v1.CreateISCSIGatewayRequest{
		Resource: "data", Iqn: "iqn.2024-01.com.example:sds.data", ServiceIp: "192.168.1.101/24",
	})
	assert.Error(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "promote boom")
}

// ==================== DeleteGateway ====================

func TestCovDeleteGateway(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(nil, dep, zap.NewNop(), []string{"node1", "node2"})

	err := m.DeleteGateway(context.Background(), "data")
	require.NoError(t, err)
	// rm commands issued for all three gateway types across both nodes plus
	// reload commands.
	joined := ""
	for _, c := range dep.ExecCommands {
		joined += c + "\n"
	}
	assert.Contains(t, joined, "sds-nfs-data.toml")
	assert.Contains(t, joined, "sds-iscsi-data.toml")
	assert.Contains(t, joined, "sds-nvmeof-data.toml")
	assert.Contains(t, joined, "reload drbd-reactor")
}

// ==================== GatewayServiceActive ====================

func TestCovGatewayServiceActive(t *testing.T) {
	// Empty node short-circuits to false.
	m := New(nil, &MockDeploymentClient{}, zap.NewNop(), nil)
	assert.False(t, m.GatewayServiceActive(context.Background(), "", "data"))

	// Exec success => active.
	assert.True(t, m.GatewayServiceActive(context.Background(), "node1", "data"))

	// Exec failure => inactive.
	mFail := New(nil, &MockDeploymentClient{ExecErr: fmt.Errorf("inactive")}, zap.NewNop(), nil)
	assert.False(t, mFail.GatewayServiceActive(context.Background(), "node1", "data"))
}

// ==================== ensureNVMeModules ====================

func TestCovEnsureNVMeModules(t *testing.T) {
	nvme := NewNVMeManager(New(nil, &MockDeploymentClient{}, zap.NewNop(), nil))

	// No nodes is a no-op.
	require.NoError(t, nvme.ensureNVMeModules(context.Background(), nil))

	// Successful modprobe path.
	require.NoError(t, nvme.ensureNVMeModules(context.Background(), []string{"node1"}))

	// Failure surfaces a helpful error.
	nvmeFail := NewNVMeManager(New(nil, &MockDeploymentClient{ExecErr: fmt.Errorf("no module")}, zap.NewNop(), nil))
	err := nvmeFail.ensureNVMeModules(context.Background(), []string{"node1"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nvmet kernel modules")
}

// ==================== List helpers backed by mock config store ====================

func TestCovListNamespacesAndPorts(t *testing.T) {
	dep := &MockDeploymentClient{}
	nvme := NewNVMeManager(New(nil, dep, zap.NewNop(), []string{"node1"}))

	req := &v1.CreateNVMeGatewayRequest{
		Resource: "res", Nqn: "nqn.2024-01.com.example:sds.res", ServiceIp: "192.168.1.150/24",
	}
	sip, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	cfg, err := nvme.generateNVMeGatewayConfig(req, sip, "/dev/drbd0", testVolumes(3))
	require.NoError(t, err)
	dep.SetConfig(gatewayConfigPath("sds-nvmeof-res"), cfg)

	ns, err := nvme.ListNamespaces(context.Background(), "res")
	require.NoError(t, err)
	require.Len(t, ns, 2)
	assert.Equal(t, "1", ns[0]["namespace_id"])

	ports, err := nvme.ListPorts(context.Background(), "res")
	require.NoError(t, err)
	require.Len(t, ports, 1)
}

// ListNamespaces returns an error when no config exists anywhere.
func TestCovListNamespacesMissingConfig(t *testing.T) {
	nvme := NewNVMeManager(New(nil, &MockDeploymentClient{}, zap.NewNop(), nil))
	_, err := nvme.ListNamespaces(context.Background(), "nope-xyz")
	assert.Error(t, err)
}

func TestCovNVMeRemoveHostErrors(t *testing.T) {
	dep := &MockDeploymentClient{}
	nvme := NewNVMeManager(New(nil, dep, zap.NewNop(), []string{"node1"}))

	req := &v1.CreateNVMeGatewayRequest{
		Resource: "res", Nqn: "nqn.2024-01.com.example:sds.res", ServiceIp: "192.168.1.150/24",
	}
	sip, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	cfg, err := nvme.generateNVMeGatewayConfig(req, sip, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	dep.SetConfig(gatewayConfigPath("sds-nvmeof-res"), cfg)

	// No explicit allow-list yet: removing a host is refused.
	err = nvme.RemoveHost(context.Background(), "res", "nqn.host")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "allows all initiators")

	// Add then remove a non-existent host => not found.
	require.NoError(t, nvme.AddHost(context.Background(), "res", "nqn.present"))
	err = nvme.RemoveHost(context.Background(), "res", "nqn.absent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "host not found")
}

// ==================== iSCSI initiator / CHAP error branches ====================

func TestCovISCSIRemoveInitiatorAndListErrors(t *testing.T) {
	dep := &MockDeploymentClient{}
	iscsi := NewISCSIManager(New(nil, dep, zap.NewNop(), []string{"node1"}))

	req := &v1.CreateISCSIGatewayRequest{
		Resource: "res", Iqn: "iqn.2024-01.com.example:sds.res", ServiceIp: "192.168.1.200/24",
	}
	sip, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	cfg, err := iscsi.generateISCSIGatewayConfig(req, sip, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	dep.SetConfig(gatewayConfigPath("sds-iscsi-res"), cfg)

	// No explicit ACL: remove is refused.
	err = iscsi.RemoveInitiator(context.Background(), "res", "iqn.x")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "allows all initiators")

	// ListInitiators on an all-allowed gateway yields "ALL".
	list, err := iscsi.ListInitiators(context.Background(), "res")
	require.NoError(t, err)
	assert.Equal(t, []string{"ALL"}, list)

	// Add two, then remove one that is absent => not found.
	require.NoError(t, iscsi.AddInitiator(context.Background(), "res", "iqn.a"))
	err = iscsi.RemoveInitiator(context.Background(), "res", "iqn.absent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Now removing the present one succeeds.
	require.NoError(t, iscsi.RemoveInitiator(context.Background(), "res", "iqn.a"))
}

func TestCovISCSIMutualCHAPUnsupported(t *testing.T) {
	iscsi := NewISCSIManager(New(nil, &MockDeploymentClient{}, zap.NewNop(), []string{"node1"}))
	err := iscsi.SetCHAP(context.Background(), "res", "u", "p", true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mutual CHAP")
}

// ==================== Simple OCF-managed / unsupported stubs ====================

func TestCovManagedByOCFStubs(t *testing.T) {
	iscsi := NewISCSIManager(New(nil, nil, zap.NewNop(), nil))
	assert.Error(t, iscsi.DeleteTarget(context.Background(), "res"))

	nvme := NewNVMeManager(New(nil, nil, zap.NewNop(), nil))
	assert.Error(t, nvme.DeleteSubsystem(context.Background(), "nqn.x"))
	assert.Error(t, nvme.CreatePort(context.Background(), "res", "10.0.0.1", 4420))
}

func TestCovDeletePortCustomPortUnsupported(t *testing.T) {
	nvme := NewNVMeManager(New(nil, &MockDeploymentClient{}, zap.NewNop(), []string{"node1"}))
	err := nvme.DeletePort(context.Background(), "res", "10.0.0.1", 9999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "custom NVMe port")
}

// ==================== config helper edge cases ====================

func TestCovConfigHelperEdges(t *testing.T) {
	// joinConfigLines with empty input, both trailing-newline branches.
	assert.Equal(t, "", joinConfigLines(nil, false))
	assert.Equal(t, "\n", joinConfigLines(nil, true))

	// splitConfigLines on empty content.
	lines, nl := splitConfigLines("")
	assert.Empty(t, lines)
	assert.False(t, nl)

	// removeLine not-found returns original slice + false.
	orig := []string{"a", "b"}
	out, ok := removeLine(orig, func(string) bool { return false })
	assert.False(t, ok)
	assert.Equal(t, orig, out)

	// parseIntParam missing / empty key errors.
	_, err := parseIntParam(map[string]string{}, "k")
	assert.Error(t, err)
	_, err = parseIntParam(map[string]string{"k": "  "}, "k")
	assert.Error(t, err)

	// insertLineBefore with no match point errors.
	_, err = insertLineBefore(orig, "x", func(string) bool { return false })
	assert.Error(t, err)
}

// volumeDevice falls back to minor arithmetic when the requested volume is not
// present in the supplied list.
func TestCovVolumeDeviceFallback(t *testing.T) {
	vols := []*ResourceVolumeInfo{{VolumeID: 0, Device: "/dev/drbd0"}}
	assert.Equal(t, "/dev/drbd0", volumeDevice(vols, "/dev/drbd0", 0))
	// Volume 2 absent from list => base minor + 2.
	assert.Equal(t, "/dev/drbd2", volumeDevice(vols, "/dev/drbd0", 2))
}

// executeTemplate surfaces parse and execute errors.
func TestCovExecuteTemplateErrors(t *testing.T) {
	_, err := executeTemplate("{{ .Unclosed ", nil)
	assert.Error(t, err)

	_, err = executeTemplate("{{ .Missing.Field }}", struct{}{})
	assert.Error(t, err)
}
