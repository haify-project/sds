package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// wanTestVolumes is a single-volume resource used by the WAN config tests.
func wanTestVolumes() []resolvedVolume {
	return []resolvedVolume{{id: 0, minor: 0, pool: "vg0", volumeName: "data_data"}}
}

// TestGenerateDrbdConfigLANUnchanged pins the LAN default: passing wan=nil must
// keep the exact pre-WAN output — protocol C, real peer IPs, no pull-ahead. This
// is the backward-compat guarantee the WAN feature must not regress.
func TestGenerateDrbdConfigLANUnchanged(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	cfg := ctrl.resources.generateDrbdConfig(
		"data", 7001, wanTestVolumes(),
		[]string{"node1", "node2"}, nil,
		"C", "lvm", nil, nil)

	assert.Contains(t, cfg, "protocol C;")
	assert.Contains(t, cfg, "address   10.0.0.1:7001;")
	assert.Contains(t, cfg, "address   10.0.0.2:7001;")
	assert.NotContains(t, cfg, "127.0.0.1")
	assert.NotContains(t, cfg, "on-congestion")
	assert.NotContains(t, cfg, "protocol A;")
}

// TestGenerateDrbdConfigWAN verifies the opt-in WAN variant: protocol A, DRBD
// pull-ahead, and loopback addresses so DRBD talks to the local per-resource
// proxy. The DR node binds `port`; the primary binds `port+9` and connects out
// to `port` (the local dialer). No real peer IP appears.
func TestGenerateDrbdConfigWAN(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	cfg := ctrl.resources.generateDrbdConfig(
		"data", 7001, wanTestVolumes(),
		[]string{"node1", "node2"}, nil,
		"C", "lvm", nil, &wanConfig{DRNode: "node2"})

	// Async protocol forced, C ignored.
	assert.Contains(t, cfg, "protocol A;")
	assert.NotContains(t, cfg, "protocol C;")

	// DRBD-level pull-ahead so the primary goes Ahead instead of blocking.
	assert.Contains(t, cfg, "on-congestion pull-ahead;")
	assert.Contains(t, cfg, "csums-alg sha256;")
	assert.Contains(t, cfg, "ping-timeout 20;")

	// Loopback routing: primary (node1) binds port+9, DR (node2) binds port.
	assert.Contains(t, cfg, "on node1 {")
	assert.Contains(t, cfg, "address   127.0.0.1:7010;")
	assert.Contains(t, cfg, "on node2 {")
	assert.Contains(t, cfg, "address   127.0.0.1:7001;")

	// The peer's real IP must never leak into a WAN resource config.
	assert.NotContains(t, cfg, "10.0.0.1")
	assert.NotContains(t, cfg, "10.0.0.2")
}

// TestGenerateDrbdConfigWANUserOverride confirms a user-supplied net option still
// wins over the WAN default (the WAN options are defaults, not hard overrides).
func TestGenerateDrbdConfigWANUserOverride(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	cfg := ctrl.resources.generateDrbdConfig(
		"data", 7001, wanTestVolumes(),
		[]string{"node1", "node2"}, nil,
		"C", "lvm", map[string]string{"net/ping-timeout": "40"}, &wanConfig{DRNode: "node2"})

	assert.Contains(t, cfg, "ping-timeout 40;")
	assert.False(t, strings.Contains(cfg, "ping-timeout 20;"),
		"user net option must override the WAN default")
}
