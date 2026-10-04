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
		// Project quotas let an NFS export directory have a quota of its own
		// (pkg/controller/nfs_quota.go); the feature can only be added to an
		// unmounted filesystem, so it is there from the start.
		cmd := fmt.Sprintf("sudo blkid %s >/dev/null 2>&1 || sudo mkfs.ext4 -q -O quota,project %s", device, device)
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

// gatewayPrereqs is what one gateway's promoter chain needs on a node: the
// ocf:heartbeat agents it names, the userspace tools those agents call, and
// the packages to install when any of them is missing.
type gatewayPrereqs struct {
	agents  []string
	tools   []string
	install string
	// checks are further shell tests, each "<test> || missing=\"$missing <what>\"".
	checks []string
}

// ocfAgentsInstall is where every gateway's agents come from. Ubuntu's
// resource-agents-base lacks Filesystem, which every chain starts with.
const ocfAgentsInstall = "resource-agents-extra (Debian/Ubuntu) or resource-agents (EL)"

// nfsPrereqs: the nfsserver agent starts the distribution's NFS server and the
// exportfs agent drives exportfs(8). Without the server package the agents are
// present, the config is written, and the promoter fails on the node with "No
// init script or systemd unit file detected for nfs server".
func nfsPrereqs() gatewayPrereqs {
	return gatewayPrereqs{
		agents:  []string{"Filesystem", "IPaddr2", "nfsserver", "exportfs"},
		tools:   []string{"rpc.nfsd", "exportfs"},
		install: "nfs-kernel-server (Debian/Ubuntu) or nfs-utils (EL)",
	}
}

// iscsiPrereqs: the iSCSITarget and iSCSILogicalUnit agents call the tool of
// the implementation they are told to use. Only LIO (lio-t, i.e. targetcli) is
// accepted — see validateISCSIImplementation — so targetcli is that tool.
func iscsiPrereqs() gatewayPrereqs {
	return gatewayPrereqs{
		agents:  []string{"Filesystem", "IPaddr2", "iSCSITarget", "iSCSILogicalUnit"},
		tools:   []string{"targetcli"},
		install: "targetcli-fb (Debian/Ubuntu) or targetcli (EL)",
	}
}

// nvmePrereqs: the nvmet-* agents drive the kernel target entirely through
// configfs (/sys/kernel/config/nvmet) and call no userspace tool, so nvmetcli
// is deliberately not required. Their real dependency is the kernel modules,
// which ensureNVMeModules loads for the chosen transport.
func nvmePrereqs() gatewayPrereqs {
	return gatewayPrereqs{
		agents: []string{"Filesystem", "IPaddr2", "nvmet-subsystem", "nvmet-namespace", "nvmet-port"},
	}
}

// gatewayNodes returns the nodes a gateway's promoter runs on — the resource's
// diskful replicas, or all its nodes when the controller cannot tell them
// apart. Those are the nodes whose prerequisites matter: a tiebreaker never
// runs the chain (see writeReactorConfig).
func gatewayNodes(res *ResourceInfo) []string {
	if res == nil {
		return nil
	}
	if len(res.Hosts) > 0 {
		return res.Hosts
	}
	return res.Nodes
}

// checkGatewayPrereqs verifies that the OCF resource agents and userspace tools
// a gateway type needs are installed on every node, so gateway creation fails
// with a clear, actionable message instead of writing a drbd-reactor promoter
// config that then silently fails to start.
//
// The probe accumulates into a shell variable, so it must go through
// runScript: sent as a plain command, dispatch's quoting expands $missing to
// nothing before the script runs and the check passes whatever is installed.
func (m *Manager) checkGatewayPrereqs(ctx context.Context, nodes []string, p gatewayPrereqs) error {
	if len(nodes) == 0 {
		return nil
	}
	if err := m.runScript(ctx, nodes, prereqScript(p)); err != nil {
		hint := ocfAgentsInstall
		if p.install != "" {
			hint += "; " + p.install
		}
		return fmt.Errorf("gateway prerequisites missing (install %s): %w", hint, err)
	}
	return nil
}

// prereqScript is the shell probe checkGatewayPrereqs runs on each node. It
// prints "missing: <items>" and exits 1 when anything is absent. Tools are
// also looked up in the sbin directories, where rpc.nfsd and exportfs live.
func prereqScript(p gatewayPrereqs) string {
	lines := []string{"missing="}
	for _, a := range p.agents {
		lines = append(lines, fmt.Sprintf(
			`test -x /usr/lib/ocf/resource.d/heartbeat/%[1]s || missing="$missing ocf:heartbeat:%[1]s"`, a))
	}
	for _, t := range p.tools {
		lines = append(lines, fmt.Sprintf(
			`command -v %[1]s >/dev/null 2>&1 || test -x /usr/sbin/%[1]s || test -x /sbin/%[1]s || missing="$missing %[1]s"`, t))
	}
	lines = append(lines, p.checks...)
	lines = append(lines, `if [ -n "$missing" ]; then echo "missing:$missing"; exit 1; fi`)
	return strings.Join(lines, "\n")
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
