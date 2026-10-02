package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// Taking a gateway in and out of service is not "start the systemd target" and
// "stop the systemd target", which is why it lives in its own file rather than
// next to the per-protocol create paths.
//
// drbd-reactor owns the service chain. As long as a promoter config sits in
// /etc/drbd-reactor.d, reactor will re-promote the resource and restart the
// whole chain within seconds of anything stopping it — so a stop that only
// touches systemd looks like it worked and is undone before the caller's next
// command. Every operation here therefore manipulates the *config file* first
// (mirroring `drbd-reactorctl disable`) and only then touches the running
// services.
//
// The two other things that are easy to get wrong, and that every function
// below depends on, are the shell quoting (see runScript) and the portblock
// residue (see flushPortblockRules). Both are invisible when they fail: the
// first silently empties shell variables, the second silently firewalls a
// gateway that reports healthy on every other check. Keeping them beside the
// callers that need them is what stops a future start/stop path from being
// written without them.

// DeleteGateway deletes a gateway configuration
func (m *Manager) DeleteGateway(ctx context.Context, id string) error {
	m.logger.Info("Deleting gateway", zap.String("id", id))

	// Take the gateway out of reactor management FIRST (same disable
	// semantics as StopGateway). Stopping the target while the config is
	// live races reactor restarting the chain — which is exactly how stale
	// portblock rules survived deletion.
	if err := m.StopGateway(ctx, id); err != nil {
		m.logger.Warn("Failed to stop gateway before deletion", zap.String("id", id), zap.Error(err))
	}

	// Remove configs on all nodes
	for _, host := range m.hosts {
		m.logger.Info("Removing gateway config on node",
			zap.String("node", host),
			zap.String("gateway", id))

		// Delete reactor config files (all types: nfs, iscsi, nvmeof)
		configFiles := []string{
			fmt.Sprintf("sds-nfs-%s.toml", id),
			fmt.Sprintf("sds-iscsi-%s.toml", id),
			fmt.Sprintf("sds-nvmeof-%s.toml", id),
		}

		for _, configFile := range configFiles {
			configPath := filepath.Join(DrbdReactorConfigDir, configFile)
			// A stopped gateway keeps its config as .toml.disabled.
			rmCmd := fmt.Sprintf("sudo rm -f %s %s.disabled", configPath, configPath)
			if err := m.deployment.Exec(ctx, []string{host}, rmCmd); err != nil {
				m.logger.Warn("Failed to remove config file", zap.String("host", host), zap.String("file", configPath), zap.Error(err))
			}
		}

		// Reload drbd-reactor to pick up changes
		if err := m.deployment.Exec(ctx, []string{host}, reloadReactorCmd); err != nil {
			m.logger.Warn("Failed to reload drbd-reactor", zap.String("host", host), zap.Error(err))
		}
	}

	m.logger.Info("Gateway deleted successfully", zap.String("id", id))
	return nil
}

// writeReactorConfig installs a gateway's promoter config on the resource's
// diskful nodes and retires it everywhere else.
//
// The config used to go to every host the controller manages. A promoter on a
// node that is not a replica can still act: a tiebreaker is connected to the
// resource, and DRBD 9 lets a diskless node become Primary with I/O served over
// the network. Once one was connected, its promoter took the resource the first
// time the diskful nodes could not, and ran the whole gateway — filesystems,
// NFS state, the service IP — on a machine with no copy of the data.
func (m *Manager) writeReactorConfig(ctx context.Context, resource, pluginID, config string) error {
	remotePath := filepath.Join(DrbdReactorConfigDir, fmt.Sprintf("%s.toml", pluginID))
	run, rest := m.promoterHosts(ctx, resource)

	m.logger.Debug("Writing reactor config",
		zap.Strings("hosts", run),
		zap.Strings("retired_on", rest),
		zap.String("path", remotePath))

	if strings.HasPrefix(pluginID, "sds-nfs-") {
		m.prepareNFSNode(ctx, run, "")
	}
	if err := m.deployment.DistributeConfig(ctx, run, config, remotePath); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	reloadCmd := reloadReactorCmd
	if err := m.deployment.Exec(ctx, run, reloadCmd); err != nil {
		m.logger.Warn("Failed to reload drbd-reactor", zap.Error(err))
	}
	m.retirePromoter(ctx, rest, resource)
	return nil
}

// promoterHosts splits the managed hosts into those that may run resource's
// gateway — its diskful replicas — and the rest. When the resource cannot be
// looked up, every host is returned as a runner and none as the rest: retiring
// a promoter on a guess could take down the only node serving the gateway.
func (m *Manager) promoterHosts(ctx context.Context, resource string) (run, rest []string) {
	all := append([]string(nil), m.hosts...)
	if m.resources == nil {
		return all, nil
	}
	res, err := m.resources.GetResource(ctx, resource)
	if err != nil || res == nil {
		return all, nil
	}
	run = res.Hosts
	if len(run) == 0 {
		run = res.Nodes
	}
	if len(run) == 0 {
		return all, nil
	}
	in := make(map[string]bool, len(run))
	for _, h := range run {
		in[h] = true
	}
	for _, h := range m.hosts {
		if !in[h] {
			rest = append(rest, h)
		}
	}
	return append([]string(nil), run...), rest
}

// retirePromoter removes resource's gateway promoter from hosts that must not
// run it, and stops the gateway there if it is running. Stopping the target
// demotes the resource, which lets a diskful node's promoter take it.
func (m *Manager) retirePromoter(ctx context.Context, hosts []string, resource string) {
	if len(hosts) == 0 {
		return
	}
	script := fmt.Sprintf(`found=
for b in /etc/drbd-reactor.d/sds-nfs-%[1]s.toml /etc/drbd-reactor.d/sds-iscsi-%[1]s.toml /etc/drbd-reactor.d/sds-nvmeof-%[1]s.toml; do
  for f in "$b" "$b.disabled"; do
    [ -e "$f" ] && rm -f "$f" && found=1
  done
done
[ -n "$found" ] || exit 0
systemctl reload drbd-reactor || systemctl restart drbd-reactor
systemctl stop '%[2]s' 2>/dev/null
true`, resource, fmt.Sprintf("drbd-services@%s.target", strings.ReplaceAll(resource, "-", "\\x2d")))
	if err := m.runScript(ctx, hosts, script); err != nil {
		m.logger.Warn("Failed to retire gateway promoter on non-replica nodes",
			zap.String("resource", resource), zap.Strings("hosts", hosts), zap.Error(err))
	}
}

// StartGateway starts a stopped gateway by re-enabling its reactor config
// (.toml.disabled -> .toml) and reloading drbd-reactor, which then promotes
// and starts the service chain on the best node.
func (m *Manager) StartGateway(ctx context.Context, id string) error {
	run, rest := m.promoterHosts(ctx, id)
	// Flush stale portblock DROP rules first (config still readable as
	// .disabled). The OCF portunblock agent's stop action deliberately
	// re-blocks the port, so every failover leaves one rule behind on the
	// old node — and a later failback finds the port silently firewalled.
	m.flushPortblockRules(ctx, run, id)
	// Gateways created before promoters were confined to the replicas still
	// have one on every host; re-enabling it there would hand the resource
	// back to a node without a copy of the data.
	m.retirePromoter(ctx, rest, id)
	m.prepareNFSNode(ctx, run, id)
	if err := m.moveClusterPrivatePath(ctx, run, id); err != nil {
		return err
	}
	if err := m.runScript(ctx, run, serviceIPLastScript(id)); err != nil {
		return fmt.Errorf("move the service IP to the end of the chain: %w", err)
	}

	enableScript := fmt.Sprintf(`for f in /etc/drbd-reactor.d/sds-nfs-%s.toml /etc/drbd-reactor.d/sds-iscsi-%s.toml /etc/drbd-reactor.d/sds-nvmeof-%s.toml; do
  [ -f "$f.disabled" ] && mv "$f.disabled" "$f"
done
true`, id, id, id)
	if err := m.runScript(ctx, run, enableScript); err != nil {
		return fmt.Errorf("failed to re-enable gateway config: %w", err)
	}
	return m.deployment.Exec(ctx, run, reloadReactorCmd)
}

// moveClusterPrivatePath moves a stopped gateway's state mount out from under
// the controller's Self-HA mount point (see DefaultClusterPrivateMountPath). It
// rewrites only a disabled config: a running gateway's promoter would restart
// its whole chain on the change, and a chain that cannot stop is what this is
// here to prevent. `gateway stop` then `gateway start` moves a running one.
//
// A node may still hold the old mount. Visible, it is unmounted here — the
// gateway is stopped, so nothing uses it. Covered by the controller's own
// mount it cannot be reached by path at all, and would keep the device open
// through the next demote; that node is named and the start refused.
func (m *Manager) moveClusterPrivatePath(ctx context.Context, hosts []string, id string) error {
	oldDir := filepath.Join(legacyClusterPrivateMountPath, id)
	newDir := filepath.Join(DefaultClusterPrivateMountPath, id)
	script := fmt.Sprintf(`old=%[1]s new=%[2]s
if awk -v p="$old" '$5 == p {f=1} END {exit !f}' /proc/self/mountinfo; then
  if mountpoint -q "$old"; then
    umount "$old" || { echo "$(hostname): cannot unmount the gateway's old state mount $old" >&2; exit 3; }
  else
    echo "$(hostname): the gateway's old state mount $old is hidden under the controller database mount %[3]s; move the controller off this node (sds ha evict sds-meta), then start the gateway again" >&2
    exit 3
  fi
fi
for f in /etc/drbd-reactor.d/sds-nfs-%[4]s.toml.disabled /etc/drbd-reactor.d/sds-iscsi-%[4]s.toml.disabled /etc/drbd-reactor.d/sds-nvmeof-%[4]s.toml.disabled; do
  [ -f "$f" ] || continue
  sed "s#=$old\([/ \"]\)#=$new\1#g" "$f" >"$f.new" && mv "$f.new" "$f"
done
true`, oldDir, newDir, legacyClusterPrivateMountPath, id)
	if err := m.runScript(ctx, hosts, script); err != nil {
		return fmt.Errorf("move gateway state mount out of %s: %w", legacyClusterPrivateMountPath, err)
	}
	return nil
}

// serviceIPLastScript moves the service IP to the end of a stopped iSCSI or
// NFS gateway's chain, where the templates now put it (see them for why). Like
// moveClusterPrivatePath it touches only a disabled config.
func serviceIPLastScript(id string) string {
	return fmt.Sprintf(`for f in /etc/drbd-reactor.d/sds-iscsi-%[1]s.toml.disabled /etc/drbd-reactor.d/sds-nfs-%[1]s.toml.disabled; do
  [ -f "$f" ] || continue
  awk '/"ocf:heartbeat:IPaddr2 / {ip = $0; next} /^[ \t]*\][ \t]*$/ && ip != "" {print ip; ip = ""} {print}' "$f" >"$f.new" && mv "$f.new" "$f"
done
`, id)
}

// StopGateway stops a gateway. Simply stopping the systemd target is not
// enough: drbd-reactor sees the resource may promote again and restarts the
// whole chain within seconds. Mirroring `drbd-reactorctl disable`, the
// reactor config is renamed to .toml.disabled first so reactor drops the
// resource, then the target is stopped for real.
func (m *Manager) StopGateway(ctx context.Context, id string) error {
	disableScript := fmt.Sprintf(`for f in /etc/drbd-reactor.d/sds-nfs-%s.toml /etc/drbd-reactor.d/sds-iscsi-%s.toml /etc/drbd-reactor.d/sds-nvmeof-%s.toml; do
  [ -f "$f" ] && mv "$f" "$f.disabled"
done
true`, id, id, id)
	if err := m.runScript(ctx, m.hosts, disableScript); err != nil {
		return fmt.Errorf("failed to disable gateway config: %w", err)
	}
	if err := m.reloadDrbdReactor(ctx); err != nil {
		return err
	}

	escapedID := strings.ReplaceAll(id, "-", "\\x2d")
	stopCmd := fmt.Sprintf("systemctl stop drbd-services@%s.target 2>/dev/null || true", escapedID)
	if err := m.deployment.Exec(ctx, m.hosts, stopCmd); err != nil {
		return err
	}
	// With reactor management gone and the target down, any DROP rule left
	// for the gateway's VIP/port is stale residue from reload/failure
	// loops; flush it so a later start isn't silently firewalled.
	m.flushPortblockRules(ctx, m.hosts, id)
	return nil
}

// runScript executes a shell script on hosts. Dispatch wraps commands in
// sh -c "..." (double quotes), so $variables are expanded by the OUTER
// shell — i.e. silently emptied — before the script runs. Base64-encoding
// the script makes it immune to that quoting chain.
func (m *Manager) runScript(ctx context.Context, hosts []string, script string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	cmd := fmt.Sprintf("echo %s | base64 -d | sudo /bin/sh", encoded)
	return m.deployment.Exec(ctx, hosts, cmd)
}

// flushPortblockRules removes accumulated portblock DROP rules for the
// gateway's VIP/port on the given hosts. The OCF portblock/portunblock pair
// is not symmetric across drbd-reactor reloads and failed start loops, so
// rules pile up and leave a port firewalled while every service shows
// healthy. The VIP/port pair is extracted from the gateway's reactor config,
// which must therefore still exist when this is called. Best-effort: a flush
// failure is logged, never fatal.
func (m *Manager) flushPortblockRules(ctx context.Context, hosts []string, id string) {
	script := fmt.Sprintf(`for b in /etc/drbd-reactor.d/sds-nfs-%s.toml /etc/drbd-reactor.d/sds-iscsi-%s.toml /etc/drbd-reactor.d/sds-nvmeof-%s.toml; do
  f=$b; [ -f "$f" ] || f=$b.disabled; [ -f "$f" ] || continue
  ip=$(grep -oE 'ip=[0-9.]+' "$f" | head -1 | cut -d= -f2)
  port=$(grep -oE 'portno=[0-9]+' "$f" | head -1 | cut -d= -f2)
  [ -n "$ip" ] && [ -n "$port" ] || continue
  while iptables -D INPUT -d "$ip" -p tcp -m multiport --dports "$port" -j DROP 2>/dev/null; do :; done
done
true`, id, id, id)
	if err := m.runScript(ctx, hosts, script); err != nil {
		m.logger.Warn("Failed to flush leftover portblock rules",
			zap.String("gateway", id), zap.Error(err))
	}
}

// reloadReactorCmd reloads drbd-reactor where it is installed. A registered
// node need not run it — a hypervisor host kept for its disks, say — and
// "unit not found" there used to fail stopping a gateway that never ran on it.
const reloadReactorCmd = "if systemctl cat drbd-reactor.service >/dev/null 2>&1; then " +
	"sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor; fi"

// reloadDrbdReactor reloads drbd-reactor configuration
func (m *Manager) reloadDrbdReactor(ctx context.Context) error {
	reloadCmd := reloadReactorCmd
	return m.deployment.Exec(ctx, m.hosts, reloadCmd)
}
