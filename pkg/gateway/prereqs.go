package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Everything here answers one question: will the promoter's start chain
// actually succeed on this node, and did it?
//
// A drbd-reactor promoter config is accepted whether or not the OCF agents it
// names exist and whether or not the volumes it mounts carry a filesystem.
// Writing one is therefore never the failure — the failure happens later, on a
// node the operator is not watching, as a promoter that quietly refuses to
// start. The three checks below turn that class of silent failure into an error
// at the point of the request:
//
//   - checkGatewayPrereqs, before the config is written, for the agents and
//     userspace tools the chain will invoke;
//   - ensureGatewayPrerequisites, before reactor takes over, for the one thing
//     the Filesystem agent will not do for itself (mkfs);
//   - GatewayServiceActive, afterwards, to tell a gateway that is really
//     serving from one whose DRBD resource is Primary but whose services never
//     came up.
//
// They are grouped rather than filed under their individual callers because
// they share that failure mode: skip any of them and the gateway looks created.

// ensureGatewayPrerequisites promotes the resource on one of ITS OWN nodes
// and makes sure the cluster-private volume (volume 0) carries a filesystem;
// the promoter's Filesystem agent only mounts, it never formats. mkfs runs
// only when blkid finds no existing filesystem, so the call is idempotent
// and never destroys data.
func (m *Manager) ensureGatewayPrerequisites(ctx context.Context, resource string, nodes []string, devices ...string) error {
	if len(nodes) == 0 {
		return fmt.Errorf("resource %s has no nodes", resource)
	}
	// A recreated gateway may inherit stale portblock DROP rules from a
	// previous incarnation (or its failovers); flush them so the freshly
	// started gateway is actually reachable.
	m.flushPortblockRules(ctx, m.hosts, resource)
	node := nodes[0]
	if err := m.resources.SetPrimary(ctx, resource, node, false); err != nil {
		return fmt.Errorf("failed to promote %s on %s: %w", resource, node, err)
	}
	// A freshly promoted resource can lose Primary for a moment when a
	// previous reactor teardown is still settling, which makes mkfs race a
	// demote. Retry briefly instead of failing the whole gateway creation.
	for _, device := range devices {
		cmd := fmt.Sprintf("sudo blkid %s >/dev/null 2>&1 || sudo mkfs.ext4 -q %s", device, device)
		var lastErr error
		formatted := false
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(2 * time.Second)
				if err := m.resources.SetPrimary(ctx, resource, node, false); err != nil {
					m.logger.Warn("Re-promote before mkfs retry failed",
						zap.String("resource", resource), zap.Error(err))
				}
			}
			if lastErr = m.deployment.Exec(ctx, []string{node}, cmd); lastErr == nil {
				formatted = true
				break
			}
		}
		if !formatted {
			return fmt.Errorf("failed to prepare filesystem on %s: %w", device, lastErr)
		}
	}
	return nil
}

// checkGatewayPrereqs verifies that the OCF resource agents and userspace tools
// a gateway type needs are installed on every node, so gateway creation fails
// with a clear, actionable message instead of writing a drbd-reactor promoter
// config that then silently fails to start (e.g. missing resource-agents-extra
// for the Filesystem agent, or targetcli for iSCSITarget).
func (m *Manager) checkGatewayPrereqs(ctx context.Context, nodes []string, agents, tools []string) error {
	if len(nodes) == 0 {
		return nil
	}
	parts := []string{"missing=''"}
	for _, a := range agents {
		parts = append(parts, fmt.Sprintf(
			"test -x /usr/lib/ocf/resource.d/heartbeat/%s || missing=\"$missing ocf:heartbeat:%s\"", a, a))
	}
	for _, t := range tools {
		parts = append(parts, fmt.Sprintf(
			"command -v %s >/dev/null 2>&1 || missing=\"$missing %s\"", t, t))
	}
	parts = append(parts, "if [ -n \"$missing\" ]; then echo \"missing:$missing\"; exit 1; fi")
	cmd := strings.Join(parts, "; ")
	if err := m.deployment.Exec(ctx, nodes, cmd); err != nil {
		return fmt.Errorf("gateway prerequisites missing (install resource-agents-extra and the target tooling): %w", err)
	}
	return nil
}

// GatewayServiceActive reports whether the drbd-reactor promoter target for a
// gateway resource is actually running on the given node — i.e. all start
// actions (mount, VIP, target, LUNs) succeeded. It distinguishes a gateway
// that is really serving from one whose DRBD resource is Primary but whose
// services failed to start (e.g. a missing OCF agent).
func (m *Manager) GatewayServiceActive(ctx context.Context, node, resource string) bool {
	if node == "" {
		return false
	}
	cmd := fmt.Sprintf("systemctl is-active drbd-services@%s.target 2>/dev/null | grep -qx active", resource)
	return m.deployment.Exec(ctx, []string{node}, cmd) == nil
}
