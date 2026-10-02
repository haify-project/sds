package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/wanproxy"
)

// registerWANNodes wires up a primary site plus a DR node.
func registerWANNodes(ctrl *Controller) {
	for _, n := range []struct{ name, addr, host string }{
		{"node-a", "192.168.1.10", "sds-a"},
		{"node-b", "192.168.1.11", "sds-b"},
		{"node-dr", "203.0.113.7", "sds-dr"},
	} {
		ctrl.nodes.nodes[n.addr] = &NodeInfo{
			Name: n.name, Address: n.addr, Hostname: n.host, State: NodeStateOnline,
		}
		ctrl.hostsMap[n.name] = n.addr
	}
}

// 两地三中心: the production site replicates synchronously between its own
// nodes, and asynchronously to one remote copy. The generated config therefore
// has to say two different things about the same node — LAN address to its
// siblings, loopback proxy to the DR — which only explicit `connection`
// sections can express.
func TestGenerateDrbdConfigMultiReplicaWAN(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerWANNodes(ctrl)

	cfg := ctrl.resources.generateDrbdConfig(
		"data", 7300,
		[]resolvedVolume{{id: 0, volumeName: "data_data", pool: "vg0", minor: 0, sizeGB: 1}},
		[]string{"node-a", "node-b", "node-dr"}, nil,
		"C", "lvm", nil,
		&wanConfig{DRNode: "node-dr", PrimaryNodes: []string{"node-a", "node-b"}},
	)

	// Primary-site replicas keep their real addresses: that is the synchronous
	// mesh, and routing it through a proxy would be pointless and slow.
	assert.Contains(t, cfg, "address   192.168.1.10:7300;")
	assert.Contains(t, cfg, "address   192.168.1.11:7300;")

	// The LAN mesh must NOT include the DR — connection-mesh would pair it with
	// the replicas on their host-stanza addresses, which cannot reach it.
	meshLine := ""
	for _, line := range strings.Split(cfg, "\n") {
		if strings.Contains(line, "hosts") && strings.Contains(line, "sds-a") {
			meshLine = line
			break
		}
	}
	require.NotEmpty(t, meshLine, "expected a primary-site connection-mesh")
	assert.Contains(t, meshLine, "sds-a")
	assert.Contains(t, meshLine, "sds-b")
	assert.NotContains(t, meshLine, "sds-dr", "the DR must not join the LAN mesh")

	// One explicit WAN leg per primary, each on its own loopback pair so the
	// two tunnels cannot collide on the DR, which terminates both.
	assert.Contains(t, cfg, "host sds-a address 127.0.0.1:7400;", "primary binds leg port + bind offset")
	assert.Contains(t, cfg, "host sds-dr address 127.0.0.1:7300;", "DR binds the leg port; the primary connects there via its dialer")
	assert.Contains(t, cfg, "host sds-b address 127.0.0.1:7401;")
	assert.Contains(t, cfg, "host sds-dr address 127.0.0.1:7301;")
	assert.Equal(t, 2, strings.Count(cfg, "connection {"), "one connection per WAN leg")

	// Each WAN leg is async regardless of the LAN protocol.
	assert.Equal(t, 2, strings.Count(cfg, "protocol A;"))
	assert.Contains(t, cfg, "on-congestion pull-ahead;")
	assert.Contains(t, cfg, "csums-alg sha256;")
	// The resource-level protocol stays synchronous for the primary site.
	assert.Contains(t, cfg, "protocol C;")
}

// A single-replica WAN resource is the shape that already shipped; it must keep
// using the loopback host addresses and gain no connection sections.
func TestGenerateDrbdConfigSingleReplicaWANUnchanged(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerWANNodes(ctrl)

	cfg := ctrl.resources.generateDrbdConfig(
		"data", 7300,
		[]resolvedVolume{{id: 0, volumeName: "data_data", pool: "vg0", minor: 0, sizeGB: 1}},
		[]string{"node-a", "node-dr"}, nil,
		"A", "lvm", nil,
		&wanConfig{DRNode: "node-dr", PrimaryNodes: []string{"node-a"}},
	)

	assert.Contains(t, cfg, "address   127.0.0.1:7309;", "primary binds port+9")
	assert.Contains(t, cfg, "address   127.0.0.1:7300;", "DR binds port")
	assert.NotContains(t, cfg, "connection {", "no explicit connections in the two-endpoint shape")
	assert.NotContains(t, cfg, "192.168.1.10", "single-replica WAN never uses real IPs")
}

// A LAN resource must be untouched by any of this.
func TestGenerateDrbdConfigLANUnaffectedByWANChanges(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerWANNodes(ctrl)

	cfg := ctrl.resources.generateDrbdConfig(
		"data", 7100,
		[]resolvedVolume{{id: 0, volumeName: "data_data", pool: "vg0", minor: 0, sizeGB: 1}},
		[]string{"node-a", "node-b"}, nil,
		"C", "lvm", nil, nil,
	)

	assert.Contains(t, cfg, "address   192.168.1.10:7100;")
	assert.Contains(t, cfg, "address   192.168.1.11:7100;")
	assert.NotContains(t, cfg, "127.0.0.1")
	assert.NotContains(t, cfg, "connection {")
}

// The DRBD side and wanproxy must agree on which port each end of a leg binds,
// or the tunnel is wired to nothing.
func TestWANLoopbackOffsetMatchesWanproxy(t *testing.T) {
	legs := wanproxy.MultiSpec{
		Resource:         "data",
		PrimaryNodeAddrs: []string{"192.168.1.10", "192.168.1.11"},
		DRNodeAddr:       "203.0.113.7",
		DRPublicEndpoint: "203.0.113.7",
		BaseWANPort:      6600,
		BaseDRBDPort:     7300,
	}.Legs()

	require.Len(t, legs, 2)
	// Leg i's dialer listens on BaseDRBDPort+i — the same port the generated
	// DRBD config tells primary i to connect to.
	assert.Equal(t, 7300, legs[0].DRBDPort)
	assert.Equal(t, 7301, legs[1].DRBDPort)
	// And each leg gets its own WAN port.
	assert.Equal(t, 6600, legs[0].WANPort)
	assert.Equal(t, 6601, legs[1].WANPort)
	// Distinct instance names, so two legs cannot share a systemd unit or a
	// config file on the DR node that terminates both.
	assert.NotEqual(t, legs[0].Resource, legs[1].Resource)
}
