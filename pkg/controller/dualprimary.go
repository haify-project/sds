package controller

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// Dual-primary support exists for ONE reason: the live-migration hand-off in
// the Proxmox VE storage plugin. During a live migration the source and target
// hypervisor both hold the disk open for a moment, which DRBD forbids unless
// allow-two-primaries is set. The plugin brackets the migration with
// SetDualPrimary(on) / SetDualPrimary(off).
//
// This is deliberately a RUNTIME-only toggle (`drbdadm net-options`), never
// written into the resource's .res file. That asymmetry is a safety feature: a
// node reboot or `drbdadm adjust` re-reads the on-disk config, which never
// contains allow-two-primaries, so the cluster self-heals back to single-primary
// even if a controller crash strands the "off" call.
//
// See docs/design/proxmox-storage-plugin.md.
const (
	dualPrimaryEnableCmdFmt  = "sudo drbdadm net-options --allow-two-primaries=yes %s"
	dualPrimaryDisableCmdFmt = "sudo drbdadm net-options --allow-two-primaries=no %s"
	dualPrimaryShowCmdFmt    = "sudo drbdsetup show %s"
)

// SetDualPrimary toggles DRBD's allow-two-primaries for a resource on every
// participating node.
//
// enable=true is strict: it refuses WAN resources outright and fails if any node
// rejects the command, because a partially-applied dual-primary is worse than
// none (DRBD needs the option on both ends of a connection, so a half-applied
// toggle produces a migration that fails midway with a confusing error).
//
// enable=false is idempotent and best-effort by design — it runs in the
// plugin's cleanup path, where the alternative to "try anyway" is leaving a
// resource stranded in dual-primary. It therefore tolerates a missing resource
// and per-node command failures, but then VERIFIES the effective state and
// reports an error if any node still has allow-two-primaries set. Tolerating the
// command while checking the outcome is what keeps "best-effort" from meaning
// "silently gave up".
func (rm *ResourceManager) SetDualPrimary(ctx context.Context, resource string, enable bool) error {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return fmt.Errorf("resource name is required")
	}
	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// WAN resources replicate with protocol A (async). Two Primaries over an
	// async link is data corruption, not a performance trade-off, so the guard
	// lives here rather than in any caller.
	if rm.controller.db != nil {
		dbRes, err := rm.controller.db.GetResource(ctx, resource)
		switch {
		case err != nil || dbRes == nil:
			// Unknown resource: for disable there is nothing to restore, so the
			// cleanup path succeeds. For enable it is a genuine error, and the
			// host lookup below produces the descriptive one.
			if !enable {
				return nil
			}
		case dbRes.WANMode && enable:
			return fmt.Errorf(
				"resource %q replicates over WAN (protocol A, asynchronous); dual-primary is refused because two Primaries over an async link corrupts data",
				resource)
		}
	}

	hosts, err := rm.dualPrimaryHosts(ctx, resource)
	if err != nil {
		if !enable {
			return nil
		}
		return err
	}
	if len(hosts) == 0 {
		if !enable {
			return nil
		}
		return fmt.Errorf("resource %q has no nodes", resource)
	}

	if enable {
		if err := rm.execAllSuccess(ctx, hosts,
			fmt.Sprintf(dualPrimaryEnableCmdFmt, resource),
			fmt.Sprintf("failed to enable dual-primary on %s", resource)); err != nil {
			return err
		}
		rm.controller.logger.Info("Enabled DRBD dual-primary (live-migration window)",
			zap.String("resource", resource), zap.Strings("hosts", hosts))
		return nil
	}

	// Disable: issue the command everywhere, ignoring per-host failures, then
	// confirm no node is left dual-primary.
	if _, err := rm.deployment.Exec(ctx, hosts, fmt.Sprintf(dualPrimaryDisableCmdFmt, resource)); err != nil {
		rm.controller.logger.Warn("Dual-primary disable command failed; verifying effective state",
			zap.String("resource", resource), zap.Error(err))
	}

	stranded, err := rm.dualPrimaryStrandedHosts(ctx, hosts, resource)
	if err != nil {
		return fmt.Errorf("failed to verify dual-primary was disabled on %s: %w", resource, err)
	}
	if len(stranded) > 0 {
		return fmt.Errorf(
			"resource %q is still in dual-primary on %v — allow-two-primaries must be cleared before the resource is used again",
			resource, stranded)
	}

	rm.controller.logger.Info("Disabled DRBD dual-primary",
		zap.String("resource", resource), zap.Strings("hosts", hosts))
	return nil
}

// dualPrimaryHosts returns every node that participates in the resource's DRBD
// mesh: replicas, quorum tiebreakers and diskless clients alike.
//
// allow-two-primaries is a per-connection net option, so it has to be set on
// both ends of every connection — including diskless clients, which is where it
// matters most here: a PVE host that contributes no disks attaches diskless and
// is exactly the node that live-migration promotes.
func (rm *ResourceManager) dualPrimaryHosts(ctx context.Context, resource string) ([]string, error) {
	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		seen[h] = true
	}

	if rm.controller.db != nil {
		if dbRes, derr := rm.controller.db.GetResource(ctx, resource); derr == nil && dbRes != nil {
			for _, node := range append(splitCSV(dbRes.DisklessNodes), splitCSV(dbRes.DisklessClients)...) {
				addr := rm.controller.ResolveHost(node)
				if addr != "" && !seen[addr] {
					seen[addr] = true
					hosts = append(hosts, addr)
				}
			}
		}
	}

	return hosts, nil
}

// dualPrimaryStrandedHosts returns the hosts whose live DRBD config still has
// allow-two-primaries enabled. `drbdsetup show` prints only non-default options,
// and an unconfigured resource prints nothing — so both "already single-primary"
// and "resource is down" read as not stranded, which is correct.
func (rm *ResourceManager) dualPrimaryStrandedHosts(ctx context.Context, hosts []string, resource string) ([]string, error) {
	result, err := rm.deployment.Exec(ctx, hosts, fmt.Sprintf(dualPrimaryShowCmdFmt, resource))
	if err != nil {
		return nil, err
	}

	var stranded []string
	for host, hr := range result.Hosts {
		if !hr.Success {
			// The show command itself failed (node unreachable). We cannot prove
			// it is clear, but we also cannot prove it is stranded; a node we
			// cannot reach is not holding the volume open either, and it
			// re-reads the .res file (no dual-primary) when it comes back.
			rm.controller.logger.Warn("Could not verify dual-primary state",
				zap.String("resource", resource), zap.String("host", host),
				zap.String("output", strings.TrimSpace(hr.Output)))
			continue
		}
		if hasAllowTwoPrimaries(hr.Output) {
			stranded = append(stranded, host)
		}
	}
	return stranded, nil
}

// hasAllowTwoPrimaries reports whether a `drbdsetup show` dump enables
// allow-two-primaries. drbdsetup renders it as `allow-two-primaries yes;`.
func hasAllowTwoPrimaries(showOutput string) bool {
	for _, line := range strings.Split(showOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "allow-two-primaries") {
			continue
		}
		if strings.Contains(line, "yes") {
			return true
		}
	}
	return false
}
