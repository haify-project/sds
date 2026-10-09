package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/haify-project/sds/pkg/deployment"
	"go.uber.org/zap"
)

// ResizeVolume resizes a DRBD volume
// hostsBelowLVSize returns the subset of hosts whose logical volume at lvPath is
// SMALLER than wantGB (or whose size could not be read). It distinguishes
// "lvresize refused because the volume is already this big" — harmless, and the
// normal state when retrying a resize whose DRBD step failed — from a real
// failure, without parsing LVM's (localised) error text.
func (rm *ResourceManager) hostsBelowLVSize(ctx context.Context, hosts []string, lvPath string, wantGB uint64) ([]string, error) {
	if len(hosts) == 0 {
		return nil, nil
	}

	cmd := fmt.Sprintf("sudo lvs --noheadings --nosuffix --units b -o lv_size %s", lvPath)
	res, err := rm.deployment.Exec(ctx, hosts, cmd)
	if err != nil {
		return nil, err
	}

	want := wantGB * 1024 * 1024 * 1024
	var short []string
	for host, hr := range res.Hosts {
		if hr == nil || !hr.Success {
			short = append(short, host)
			continue
		}
		got, perr := strconv.ParseUint(strings.TrimSpace(hr.Output), 10, 64)
		if perr != nil || got < want {
			short = append(short, host)
		}
	}
	sort.Strings(short)
	return short, nil
}

// firstFailureOutput returns the output of one failed host, for error messages
// that would otherwise carry only a list of addresses.
func firstFailureOutput(res *deployment.ExecResult) string {
	if res == nil {
		return ""
	}
	for _, host := range res.FailedHosts() {
		if hr := res.Hosts[host]; hr != nil {
			if out := strings.TrimSpace(hr.Output); out != "" {
				return out
			}
		}
	}
	return "no output"
}

func (rm *ResourceManager) ResizeVolume(ctx context.Context, resource string, volumeID uint32, newSizeGB uint64) error {
	return rm.ResizeVolumeOptions(ctx, resource, volumeID, newSizeGB, false)
}

// ResizeVolumeOptions is ResizeVolume; ignoreFreeSpace grows the volume
// although a replica's pool has less free space than the growth.
func (rm *ResourceManager) ResizeVolumeOptions(ctx context.Context, resource string, volumeID uint32, newSizeGB uint64, ignoreFreeSpace bool) error {
	return rm.resizeVolume(ctx, resource, volumeID, newSizeGB, 0, ignoreFreeSpace)
}

// resizeVolume grows a volume to newSizeGB of backing storage; with
// exactBytes the DRBD device is then set to exactly that size (exact_size.go).
func (rm *ResourceManager) resizeVolume(ctx context.Context, resource string, volumeID uint32, newSizeGB, exactBytes uint64, ignoreFreeSpace bool) error {
	rm.controller.logger.Info("Resizing volume",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.Uint64("new_size_gb", newSizeGB),
		zap.Uint64("exact_bytes", exactBytes))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}

	result, err := rm.deployment.Exec(ctx, []string{hosts[0]}, fmt.Sprintf("cat /etc/drbd.d/%s.res", resource))
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var hostResult *deployment.HostResult
	for _, r := range result.Hosts {
		hostResult = r
		break
	}
	if hostResult == nil || !hostResult.Success {
		return fmt.Errorf("failed to get config")
	}

	var target *resourceConfigVolume
	for _, volume := range parseResourceConfigVolumes(hostResult.Output) {
		if volume.VolumeID == int(volumeID) {
			v := volume
			target = &v
			break
		}
	}
	if target == nil {
		return fmt.Errorf("volume %d not found", volumeID)
	}

	if !ignoreFreeSpace {
		if err := rm.assertPoolRoomForGrowth(ctx, resource, volumeID, newSizeGB); err != nil {
			return err
		}
	}
	if err := rm.assertResizeQuota(ctx, resource, volumeID, newSizeGB, hosts); err != nil {
		return err
	}

	exactBytes, err = rm.exactResizeBytes(ctx, resource, volumeID, newSizeGB, exactBytes)
	if err != nil {
		return err
	}
	// The backing volume grows to the size creation would have given it: the
	// data plus DRBD's internal metadata. A plain "<n>G" leaves the DRBD device
	// short of <n> GiB, and a hypervisor that then asks for exactly <n> GiB
	// (QEMU's block_resize after a Proxmox disk resize) is refused.
	sizeArg := fmt.Sprintf("%dB", backingVolumeSizeBytes(uint32(newSizeGB), len(hosts)-1, false))
	if strings.HasPrefix(target.DiskPath, "/dev/zvol/") {
		volumePath := strings.TrimPrefix(target.DiskPath, "/dev/zvol/")
		zfsRes, err := rm.deployment.ZFSResizeVolume(ctx, hosts, volumePath, sizeArg)
		if err != nil {
			return fmt.Errorf("failed to resize ZFS backing volume: %w", err)
		}
		if !zfsRes.AllSuccess() {
			return fmt.Errorf("ZFS backing volume resize failed: %s", zfsRes.FailureDetails())
		}
	} else {
		// With a crypt layer the LV and the mapping are two different devices
		// and both have to grow: lvresize addresses the LV, and the mapping —
		// which is what DRBD measures — stays at its old size until cryptsetup
		// is told, so skipping that step makes `drbdadm resize` find nothing
		// new and report success on a resize that did not happen.
		lvPath, cryptPool, cryptVolume, rerr := rm.backingLVFor(ctx, resource, volumeID, target.DiskPath)
		if rerr != nil {
			return rerr
		}
		if cryptPool != "" {
			// The crypt header lives at the front of the LV and is not part of
			// the mapping, so the LV must be grown by that much more for the
			// DRBD device to reach the requested size.
			sizeArg = fmt.Sprintf("%dB", backingVolumeSizeBytes(uint32(newSizeGB), len(hosts)-1, true))
		}
		resizeCmd := fmt.Sprintf("sudo lvresize -L %s -y %s", sizeArg, lvPath)
		lvRes, err := rm.deployment.Exec(ctx, hosts, resizeCmd)
		if err != nil {
			return fmt.Errorf("failed to resize LVM backing volume: %w", err)
		}
		if !lvRes.AllSuccess() {
			// lvresize EXITS NON-ZERO when the LV is already the requested size.
			// That matters because this operation is not atomic: if the DRBD
			// step below fails (e.g. the volume is still doing its initial sync,
			// where DRBD refuses to resize), the LVs are already grown. Without
			// this check every later retry would fail here forever and the
			// volume could never be resized again.
			short, verr := rm.hostsBelowLVSize(ctx, lvRes.FailedHosts(), lvPath, newSizeGB)
			if verr != nil {
				return fmt.Errorf("LVM backing volume resize failed on %v (size could not be verified: %w)",
					lvRes.FailedHosts(), verr)
			}
			if len(short) > 0 {
				return fmt.Errorf("LVM backing volume resize failed on %v: %s",
					short, firstFailureOutput(lvRes))
			}
			rm.controller.logger.Info("LVM backing volume was already at the requested size",
				zap.String("resource", resource),
				zap.Uint64("size_gb", newSizeGB),
				zap.Strings("hosts", lvRes.FailedHosts()))
		}

		// Grow the mapping onto the extents the LV just gained. Idempotent, so
		// it is safe on the retry path the LV branch above exists to allow.
		if cryptPool != "" {
			cmd, cerr := luksResizeCmd(cryptPool, cryptVolume)
			if cerr != nil {
				return cerr
			}
			if cerr := execFailure(rm.deployment.Exec(ctx, hosts, cmd)); cerr != nil {
				return fmt.Errorf("grow the LUKS container of %s/%d onto the new extents: %w",
					resource, volumeID, cerr)
			}
		}
	}

	resizeCmd := fmt.Sprintf("sudo drbdadm resize %s/%d", resource, volumeID)
	if exactBytes > 0 {
		// The size is written to every participant's config first, or the
		// next adjust would take the device back to what the file says.
		if _, _, err := rm.stageResourceConfig(ctx, resource, func(conf string) (string, error) {
			return setVolumeSizeInConfig(conf, int(volumeID), exactBytes)
		}); err != nil {
			return fmt.Errorf("write the new size of %s/%d into its config: %w", resource, volumeID, err)
		}
		resizeCmd = fmt.Sprintf("sudo drbdadm resize --size=%s %s/%d", drbdSizeSectors(exactBytes), resource, volumeID)
	}
	drbdRes, err := rm.deployment.Exec(ctx, []string{hosts[0]}, resizeCmd)
	if err != nil {
		return fmt.Errorf("failed to resize DRBD volume: %w", err)
	}
	if !drbdRes.AllSuccess() {
		// Include the command output: the usual cause is that the volume is not
		// UpToDate everywhere yet (DRBD refuses to resize mid-resync), and the
		// bare host list gives the operator no way to know that waiting fixes
		// it. The backing LVs are already grown at this point, so a retry once
		// the resync finishes completes the resize.
		return fmt.Errorf("DRBD volume resize failed on %v: %s",
			drbdRes.FailedHosts(), firstFailureOutput(drbdRes))
	}

	if rm.controller.db != nil {
		dbVolumes, err := rm.controller.db.ListVolumes(ctx, resource)
		if err == nil {
			if volume := findVolumeRecord(dbVolumes, volumeID); volume != nil {
				volume.SizeGB = int(newSizeGB)
				volume.SizeBytes = int64(exactBytes)
				if err := rm.controller.db.SaveVolume(ctx, volume); err != nil {
					rm.controller.logger.Warn("Failed to update volume metadata",
						zap.String("resource", resource),
						zap.String("volume", volume.VolumeName),
						zap.Error(err))
				}
			}
		}
	}

	return nil
}
