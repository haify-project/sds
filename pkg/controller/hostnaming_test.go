package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/liliang-cn/sds/pkg/deployment"
)

// A DRBD .res file only applies to a host that finds itself in one of its
// `on <name>` sections. Those names must be the nodes' real hostnames, not the
// operator-chosen SDS node names — otherwise drbdadm rejects the whole resource
// with "'<res>' not defined in your config (for this host)" and every create
// fails on a cluster where the two happen to differ.
func TestGenerateDrbdConfigUsesHostnamesNotNodeNames(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)

	// Node names deliberately differ from hostnames, as they do whenever a host
	// is registered under a label of the operator's choosing.
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{
		Name: "node-a", Address: "10.0.0.1", Hostname: "lima-sds-a", State: NodeStateOnline,
	}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{
		Name: "node-b", Address: "10.0.0.2", Hostname: "sds-b", State: NodeStateOnline,
	}
	ctrl.hostsMap["node-a"] = "10.0.0.1"
	ctrl.hostsMap["node-b"] = "10.0.0.2"

	cfg := ctrl.resources.generateDrbdConfig(
		"data", 7100,
		[]resolvedVolume{{id: 0, volumeName: "data_data", pool: "vg0", minor: 0, sizeGB: 1}},
		[]string{"node-a", "node-b"}, nil,
		"C", "lvm", nil, nil,
	)

	assert.Contains(t, cfg, "on lima-sds-a {", "must key the section by hostname")
	assert.Contains(t, cfg, "on sds-b {", "must key the section by hostname")
	assert.NotContains(t, cfg, "on node-a {", "SDS node name must not reach DRBD")
	assert.NotContains(t, cfg, "on node-b {", "SDS node name must not reach DRBD")
}

// The connection-mesh lists DRBD host names for the same reason the `on`
// sections do; a mesh naming nodes DRBD has never heard of is rejected too.
func TestGenerateDrbdConfigMeshUsesHostnames(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)

	for _, n := range []struct{ name, addr, host string }{
		{"node-a", "10.0.0.1", "lima-sds-a"},
		{"node-b", "10.0.0.2", "sds-b"},
		{"node-d", "10.0.0.4", "sds-d"},
	} {
		ctrl.nodes.nodes[n.addr] = &NodeInfo{
			Name: n.name, Address: n.addr, Hostname: n.host, State: NodeStateOnline,
		}
		ctrl.hostsMap[n.name] = n.addr
	}

	cfg := ctrl.resources.generateDrbdConfig(
		"data", 7100,
		[]resolvedVolume{{id: 0, volumeName: "data_data", pool: "vg0", minor: 0, sizeGB: 1}},
		[]string{"node-a", "node-b"}, []string{"node-d"},
		"C", "lvm", nil, nil,
	)

	meshLine := ""
	for _, line := range strings.Split(cfg, "\n") {
		if strings.Contains(line, "hosts") {
			meshLine = line
			break
		}
	}
	require.NotEmpty(t, meshLine, "expected a connection-mesh hosts line")
	assert.Contains(t, meshLine, "lima-sds-a")
	assert.Contains(t, meshLine, "sds-b")
	assert.Contains(t, meshLine, "sds-d")
	assert.NotContains(t, meshLine, "node-a")
}

// A node registered without a hostname (or a caller that already passes one)
// must keep working: fall back to the reference itself rather than emitting an
// empty `on  {` section.
func TestGetDRBDNameByRefFallsBackToRef(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node-a", Address: "10.0.0.1", State: NodeStateOnline}

	assert.Equal(t, "node-a", ctrl.nodes.GetDRBDNameByRef("node-a"), "no hostname recorded")
	assert.Equal(t, "stranger", ctrl.nodes.GetDRBDNameByRef("stranger"), "unknown node")

	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{
		Name: "node-b", Address: "10.0.0.2", Hostname: "sds-b", State: NodeStateOnline,
	}
	assert.Equal(t, "sds-b", ctrl.nodes.GetDRBDNameByRef("node-b"), "by name")
	assert.Equal(t, "sds-b", ctrl.nodes.GetDRBDNameByRef("10.0.0.2"), "by address")
	assert.Equal(t, "sds-b", ctrl.nodes.GetDRBDNameByRef("sds-b"), "by hostname")
}

// Device minors are a node-global namespace and a resource's minor has to be
// free on EVERY node it spans. Probing only the first node hands back a minor
// another participating node already uses, and drbdadm then rejects the config
// with "conflicting use of device-minor" at create-md time.
func TestNextGlobalMinorScansAllHosts(t *testing.T) {
	dep := &fakeDeploymentClient{}
	// node1 knows minors 0; node2 additionally carries an unrelated resource on
	// minor 1 (a WAN/DR pair, say). The next free minor is therefore 2.
	dep.execFunc = func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		res := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
		for _, h := range hosts {
			out := "        device    minor 0;\n"
			if h == "10.0.0.2" {
				out += "        device    minor 1;\n"
			}
			res.Hosts[h] = &deployment.HostResult{Host: h, Output: out, Success: true}
		}
		return res, nil
	}
	ctrl := newBasicTestController(dep)

	minor, err := ctrl.resources.nextGlobalMinor(context.Background(), []string{"10.0.0.1", "10.0.0.2"})
	require.NoError(t, err)
	assert.Equal(t, 2, minor, "must clear the highest minor across all hosts")

	// Probing only the first host is exactly the bug: it would answer 1, which
	// collides on 10.0.0.2.
	only, err := ctrl.resources.nextGlobalMinor(context.Background(), []string{"10.0.0.1"})
	require.NoError(t, err)
	assert.Equal(t, 1, only)
}

func TestNextGlobalMinorRejectsNoHosts(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	_, err := ctrl.resources.nextGlobalMinor(context.Background(), nil)
	assert.Error(t, err, "allocating a minor with no hosts to check is a caller bug")
}

// `drbdadm --version` prints DRBD_KERNEL_VERSION=0 when the drbd kernel module
// is absent — installing only drbd-utils succeeds and leaves the node unable to
// carry any resource. Reporting "DRBD installed, version 0" makes health-check
// pass on a node where `drbdadm up` will fail with "Module drbd not found".
func TestHealthCheckTreatsMissingKernelModuleAsNotInstalled(t *testing.T) {
	utilsOnly := "DRBDADM_BUILDTAG=GIT-hash:abc\n" +
		"DRBDADM_API_VERSION=2\n" +
		"DRBD_KERNEL_VERSION_CODE=0x000000\n" +
		"DRBD_KERNEL_VERSION=0\n" +
		"DRBDADM_VERSION=9.34.0\n"
	assert.Equal(t, "0", parseVersion(utilsOnly),
		"parseVersion reports the kernel version, which is what a node needs")

	loaded := strings.Replace(utilsOnly, "DRBD_KERNEL_VERSION=0", "DRBD_KERNEL_VERSION=9.3.2", 1)
	assert.Equal(t, "9.3.2", parseVersion(loaded))
}
