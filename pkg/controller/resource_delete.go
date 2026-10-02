package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/haify-project/sds/pkg/wanproxy"
	"go.uber.org/zap"
)

// DeleteResource deletes a DRBD resource from all nodeAddresses
func (rm *ResourceManager) DeleteResource(ctx context.Context, name string, force bool) error {
	rm.controller.logger.Info("Deleting DRBD resource",
		zap.String("name", name),
		zap.Bool("force", force))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// If this resource has an HA config (a drbd-reactor promoter + VIP), tear it
	// down first — otherwise deleting the resource orphans the HA reactor config
	// and its DB record, which then lingers in the UI referencing a gone
	// resource.
	if rm.controller.db != nil {
		if ha, herr := rm.controller.db.GetHaConfig(ctx, name); herr == nil && ha != nil {
			if err := rm.RemoveHa(ctx, name); err != nil {
				rm.controller.logger.Warn("Failed to remove HA config during resource delete (continuing)",
					zap.String("resource", name), zap.Error(err))
			}
		}
	}

	// If this resource still exports a gateway (NFS/iSCSI/NVMe-oF), tear it
	// down first. The gateway's drbd-reactor promoter holds the DRBD device
	// Primary (keeping its LVs open) via drbd-services@<res>.target; deleting
	// the resource without removing the gateway config would leave that
	// promoter + target ACTIVE and the device UP, orphaning the resource as a
	// live device we could no longer bring down or lvremove. This mirrors the
	// HA cascade above and reuses the exact teardown Server.DeleteGateway runs
	// (Manager.DeleteGateway removes the reactor configs and stops the target;
	// DeleteGatewayByResource drops the DB record).
	if rm.controller.db != nil {
		if gw, gerr := rm.controller.db.GetGatewayByResource(ctx, name); gerr == nil && gw != nil {
			if rm.controller.gateway != nil {
				if err := rm.controller.gateway.DeleteGateway(ctx, name); err != nil {
					rm.controller.logger.Warn("Failed to tear down gateway during resource delete (continuing)",
						zap.String("resource", name), zap.Error(err))
				}
			}
			if err := rm.controller.db.DeleteGatewayByResource(ctx, name); err != nil {
				rm.controller.logger.Warn("Failed to delete gateway record during resource delete (continuing)",
					zap.String("resource", name), zap.Error(err))
			}
		}
	}

	hosts, err := rm.resourceHosts(ctx, name)
	if err != nil {
		return err
	}

	// Diskless quorum tiebreakers carry the config and a kernel resource but no
	// backing volume. They must be torn down too, or the config and minor/port
	// linger in the kernel as an orphan that blocks reusing them later.
	allHosts := append(append([]string(nil), hosts...), rm.disklessHosts(ctx, name)...)

	// Best-effort unmount of the resource's DRBD devices so a mounted resource
	// can be brought down — drbdadm down fails on a busy (mounted) device,
	// which would otherwise leave the mount and block teardown.
	_, _ = rm.deployment.Exec(ctx, allHosts,
		fmt.Sprintf("for d in /dev/drbd/by-res/%s/*; do sudo umount \"$d\" 2>/dev/null; done; true", name))

	// 1. Down resource on all nodes (diskful + diskless tiebreaker)
	downResult, err := rm.deployment.DRBDDown(ctx, allHosts, name)
	if err != nil {
		return fmt.Errorf("failed to bring down resource: %w", err)
	}

	if !downResult.AllSuccess() && !force {
		return fmt.Errorf("resource down failed on hosts: %s", downResult.FailureDetails())
	}

	// 1a. WAN only: tear down the per-resource sds-proxy pair AFTER `drbdadm
	// down` (the proxy must outlive DRBD's connection, mirroring the
	// Provision-before-up ordering). Best-effort: a failure here must not block
	// the delete, matching the state-LV sweep below. Gated behind WANMode.
	if rm.controller.db != nil {
		if dbRes, derr := rm.controller.db.GetResource(ctx, name); derr == nil && dbRes != nil && dbRes.WANMode {
			primaryAddrs, drAddr := rm.wanPrimaryAddrs(dbRes)
			multi := wanproxy.MultiSpec{
				Resource:         name,
				PrimaryNodeAddrs: primaryAddrs,
				DRNodeAddr:       drAddr,
				BaseWANPort:      dbRes.WANPort,
				BaseDRBDPort:     dbRes.Port,
			}
			if derr := wanproxy.DeprovisionMulti(ctx, rm.wanproxyDeployClient(), multi); derr != nil {
				rm.controller.logger.Warn("Best-effort WAN proxy deprovision failed during resource delete",
					zap.String("resource", name), zap.Error(derr))
			} else {
				rm.controller.logger.Info("Deprovisioned WAN replication proxy",
					zap.String("resource", name))
			}
		}
	}

	// 2. Delete config file from all nodes
	err = rm.deployment.DeleteConfig(ctx, allHosts, fmt.Sprintf("/etc/drbd.d/%s.res", name))
	if err != nil {
		return fmt.Errorf("failed to delete config: %w", err)
	}

	// 3. Delete backing volumes. This must happen BEFORE the database
	// records go away — they are the only remaining knowledge of which
	// LVs/zvols belong to this resource. Failures abort unless force is
	// set, so the records survive for a retry instead of leaking storage.
	if rm.controller.db != nil {
		volumes, listErr := rm.controller.db.ListVolumes(ctx, name)
		if listErr != nil {
			rm.controller.logger.Warn("Failed to list volumes for backing cleanup",
				zap.String("resource", name),
				zap.Error(listErr))
		}
		for _, volume := range volumes {
			if err := rm.deleteBackingVolume(ctx, hosts, volume); err != nil {
				if !force {
					return fmt.Errorf("failed to remove backing volume %s/%s: %w; the resource's records are kept, "+
						"so free the volume on that node (lvremove or zfs destroy) and rerun `sds resource delete %s`",
						volume.Pool, volume.VolumeName, err, name)
				}
				rm.controller.logger.Warn("Failed to remove backing volume (force: continuing)",
					zap.String("resource", name),
					zap.String("volume", volume.VolumeName),
					zap.Error(err))
			}
		}

		// Sweep any orphaned gateway state-volume LVs. A gateway auto-provisions
		// cluster-private volumes named "<res>_state<N>". If such an add did not
		// finish (or its DB record was lost), the volume above cannot see it and
		// its LV would leak on the diskful nodes. Enumerate each backing pool for
		// leftover "<res>_state*" LVs and remove them. Best-effort: a failure
		// here must not block the delete.
		poolsSwept := make(map[string]bool)
		for _, volume := range volumes {
			if volume.Pool == "" || poolsSwept[volume.Pool] || strings.HasPrefix(volume.Device, "/dev/zvol/") {
				continue
			}
			poolsSwept[volume.Pool] = true
			sweepCmd := fmt.Sprintf(
				"for lv in $(sudo lvs --noheadings -o lv_name %s 2>/dev/null | tr -d ' ' | grep -E '^%s_state[0-9]+$'); do sudo lvremove -f %s/$lv; done; true",
				volume.Pool, name, volume.Pool)
			if _, err := rm.deployment.Exec(ctx, hosts, sweepCmd); err != nil {
				rm.controller.logger.Warn("Best-effort gateway state-volume LV sweep failed",
					zap.String("resource", name),
					zap.String("pool", volume.Pool),
					zap.Error(err))
			}
		}

		for _, volume := range volumes {
			if err := rm.controller.db.DeleteVolume(ctx, name, volume.VolumeName); err != nil {
				rm.controller.logger.Warn("Failed to delete volume from database",
					zap.String("resource", name),
					zap.String("volume", volume.VolumeName),
					zap.Error(err))
			}
		}
		if err := rm.controller.db.DeleteResource(ctx, name); err != nil {
			rm.controller.logger.Warn("Failed to delete resource from database",
				zap.String("name", name),
				zap.Error(err))
		}
	}

	rm.controller.logger.Info("Resource deleted successfully",
		zap.String("name", name))

	return nil
}
