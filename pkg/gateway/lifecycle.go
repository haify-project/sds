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
		if err := m.deployment.Exec(ctx, []string{host}, "sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor"); err != nil {
			m.logger.Warn("Failed to reload drbd-reactor", zap.String("host", host), zap.Error(err))
		}
	}

	m.logger.Info("Gateway deleted successfully", zap.String("id", id))
	return nil
}

// writeReactorConfig writes drbd-reactor configuration to all nodes
func (m *Manager) writeReactorConfig(ctx context.Context, resource, pluginID, config string) error {
	remotePath := filepath.Join(DrbdReactorConfigDir, fmt.Sprintf("%s.toml", pluginID))

	m.logger.Debug("Writing reactor config to all nodes",
		zap.Strings("hosts", m.hosts),
		zap.String("path", remotePath))

	if err := m.deployment.DistributeConfig(ctx, m.hosts, config, remotePath); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Reload drbd-reactor on all nodes
	reloadCmd := "sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor"
	if err := m.deployment.Exec(ctx, m.hosts, reloadCmd); err != nil {
		m.logger.Warn("Failed to reload drbd-reactor", zap.Error(err))
	}

	return nil
}

// StartGateway starts a stopped gateway by re-enabling its reactor config
// (.toml.disabled -> .toml) and reloading drbd-reactor, which then promotes
// and starts the service chain on the best node.
func (m *Manager) StartGateway(ctx context.Context, id string) error {
	// Flush stale portblock DROP rules first (config still readable as
	// .disabled). The OCF portunblock agent's stop action deliberately
	// re-blocks the port, so every failover leaves one rule behind on the
	// old node — and a later failback finds the port silently firewalled.
	m.flushPortblockRules(ctx, m.hosts, id)

	enableScript := fmt.Sprintf(`for f in /etc/drbd-reactor.d/sds-nfs-%s.toml /etc/drbd-reactor.d/sds-iscsi-%s.toml /etc/drbd-reactor.d/sds-nvmeof-%s.toml; do
  [ -f "$f.disabled" ] && mv "$f.disabled" "$f"
done
true`, id, id, id)
	if err := m.runScript(ctx, m.hosts, enableScript); err != nil {
		return fmt.Errorf("failed to re-enable gateway config: %w", err)
	}
	return m.reloadDrbdReactor(ctx)
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

// reloadDrbdReactor reloads drbd-reactor configuration
func (m *Manager) reloadDrbdReactor(ctx context.Context) error {
	reloadCmd := "sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor"
	return m.deployment.Exec(ctx, m.hosts, reloadCmd)
}
