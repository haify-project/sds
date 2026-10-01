package controller

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// drbdBootUnit is the name of the native systemd oneshot we install to bring
// all DRBD resources up at boot. We deliberately do NOT reuse the packaged
// drbd.service: on Ubuntu 24.04 / DRBD 9 that is an LSB/SysV init script whose
// Default-Start header is empty, so `systemctl enable drbd.service` fails with
// "Default-Start contains no runlevels, aborting" and the unit can never be
// enabled. Our own native unit sidesteps that entirely.
const drbdBootUnit = "sds-drbd-up.service"

// drbdBootUnitPath is where the generated unit is written on each node.
const drbdBootUnitPath = "/etc/systemd/system/sds-drbd-up.service"

// drbdBootScriptPath is the helper script ExecStart runs. Keeping the logic in
// a script (rather than an inline ExecStart) lets it activate LVM first and
// reconcile each resource tolerantly at boot.
const drbdBootScriptPath = "/usr/local/sbin/sds-drbd-up.sh"

// drbdBootUnitInstallCmd renders the single shell command that (idempotently)
// installs and enables the DRBD boot bring-up on a node: a helper script plus a
// oneshot systemd unit that runs it before drbd-reactor. The script:
//   - activates LVM volume groups (`vgchange -ay`) so DRBD backing devices
//     exist before attach — otherwise a resource comes up Diskless because its
//     backing LV was not yet active at boot;
//   - opens any LUKS containers SDS has registered on the node, between those
//     two steps: an encrypted resource's backing device is the crypt mapping,
//     which cannot exist before the LV does and must exist before DRBD
//     attaches. Doing it inside this one script is what makes that ordering
//     certain — as three separate units it would depend on cryptsetup.target
//     landing after an LVM activation this script does not trust in the first
//     place;
//   - adjusts EACH resource independently with `|| true`, so a foreign resource
//     already brought up by its own drbd-reactor promoter (which fails with
//     "minor exists" / exit 10) can neither abort the remaining resources nor
//     fail the unit. `drbdadm` is discovered inside the script at runtime.
func drbdBootUnitInstallCmd() string {
	return `set -e
sudo tee ` + drbdBootScriptPath + ` > /dev/null <<'EOSCRIPT'
#!/bin/sh
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/sbin:/usr/bin:/bin
export PATH
DRBDADM="$(command -v drbdadm 2>/dev/null || true)"
[ -n "$DRBDADM" ] || DRBDADM=/usr/sbin/drbdadm
# 1. Activate LVM so DRBD backing devices exist before we attach them.
vgchange -ay >/dev/null 2>&1 || true
udevadm settle >/dev/null 2>&1 || true
` + luksBootOpenSnippet() + `
# 2. Reconcile each resource independently; tolerate ones already up (a foreign
#    reactor-managed resource yields "minor exists" / exit 10).
for res in $("$DRBDADM" sh-resources 2>/dev/null); do
  "$DRBDADM" adjust "$res" >/dev/null 2>&1 || true
done
exit 0
EOSCRIPT
sudo chmod +x ` + drbdBootScriptPath + `
sudo tee ` + drbdBootUnitPath + ` > /dev/null <<'EOF'
[Unit]
Description=Bring up all SDS DRBD resources at boot
After=network-online.target lvm2-monitor.service local-fs.target
Wants=network-online.target
Before=drbd-reactor.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=` + drbdBootScriptPath + `

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable ` + drbdBootUnit
}

// ensureDRBDBootUnitEnabled installs and enables the DRBD boot unit on the
// given nodes so their resources auto-come-up (`drbdadm adjust all`) after a
// reboot and re-sync without manual intervention; drbd-reactor then promotes
// once the resources are up. `adjust all` (not `up all`) is used deliberately:
// it is idempotent and reconciles config->running state, so it attaches backing
// disks AND tolerates a resource that is already up (e.g. a non-sds DRBD
// resource on the same node) instead of aborting with a "minor exists" error
// and leaving later resources half-up (Diskless). Writing the same unit file and
// re-enabling it are idempotent, so repeated calls across resource creations are
// safe.
//
// The failure is logged here and also returned, because how much it matters
// depends on the caller. For a plaintext resource it is cosmetic — the resource
// is already up, and a missing boot unit costs one `drbdadm adjust` after a
// reboot. For an encrypted one this same unit is what opens the crypt
// containers, so its absence means the node comes back with no backing device
// at all; that caller treats the error as fatal.
func (rm *ResourceManager) ensureDRBDBootUnitEnabled(ctx context.Context, hosts []string) error {
	if rm.deployment == nil || len(hosts) == 0 {
		return nil
	}
	result, err := rm.deployment.Exec(ctx, hosts, drbdBootUnitInstallCmd())
	if err != nil {
		rm.controller.logger.Warn("Failed to install DRBD boot unit; nodes may not auto-up resources after reboot",
			zap.String("unit", drbdBootUnit),
			zap.Strings("hosts", hosts),
			zap.Error(err))
		return fmt.Errorf("install %s on %v: %w", drbdBootUnit, hosts, err)
	}
	if !result.AllSuccess() {
		rm.controller.logger.Warn("Failed to install DRBD boot unit on some hosts; those nodes may not auto-up resources after reboot",
			zap.String("unit", drbdBootUnit),
			zap.Strings("failed_hosts", result.FailedHosts()))
		return fmt.Errorf("install %s on %v: %s", drbdBootUnit, result.FailedHosts(), result.FailureDetails())
	}
	rm.controller.logger.Info("Installed and enabled DRBD boot unit for reboot auto-recovery",
		zap.String("unit", drbdBootUnit),
		zap.Strings("hosts", hosts))
	return nil
}
