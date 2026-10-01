package controller

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// Mount mounts a DRBD device
func (rm *ResourceManager) Mount(ctx context.Context, resource, mountPoint string, volumeID uint32, node, fsType string) error {
	// Resolve node to address; empty means the current Primary.
	address, err := rm.resolveNodeOrPrimary(ctx, resource, node)
	if err != nil {
		return fmt.Errorf("resolve target node: %w", err)
	}

	rm.controller.logger.Info("Mounting resource",
		zap.String("resource", resource),
		zap.String("mount_point", mountPoint),
		zap.Uint32("volume_id", volumeID),
		zap.String("node", node),
		zap.String("address", address),
		zap.String("fstype", fsType))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	// Create mount point
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s", mountPoint)
	_, err = rm.deployment.Exec(ctx, []string{address}, mkdirCmd)
	if err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}

	// Mount
	mountCmd := fmt.Sprintf("sudo mount %s %s", drbdDevice, mountPoint)
	result, err := rm.deployment.Exec(ctx, []string{address}, mountCmd)
	if err != nil {
		return fmt.Errorf("failed to mount: %w", err)
	}
	if !result.AllSuccess() {
		return fmt.Errorf("mount failed on %s: %s", node, result.FailureDetails())
	}

	return nil
}

// Unmount unmounts a DRBD device
func (rm *ResourceManager) Unmount(ctx context.Context, resource string, volumeID uint32, node string) error {
	// Resolve node to address; empty means the current Primary.
	address, err := rm.resolveNodeOrPrimary(ctx, resource, node)
	if err != nil {
		return fmt.Errorf("resolve target node: %w", err)
	}

	rm.controller.logger.Info("Unmounting resource",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.String("node", node),
		zap.String("address", address))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// Unmount by device path is safer if we know volume ID
	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	umountCmd := fmt.Sprintf("sudo umount %s", drbdDevice)
	result, err := rm.deployment.Exec(ctx, []string{address}, umountCmd)
	if err != nil {
		return fmt.Errorf("failed to unmount: %w", err)
	}
	if !result.AllSuccess() {
		return fmt.Errorf("unmount failed on %s: %s", node, result.FailureDetails())
	}

	return nil
}

// generateSystemdMountUnit generates a systemd mount unit content
func (rm *ResourceManager) generateSystemdMountUnit(resource, mountPoint, fsType string) string {
	device := fmt.Sprintf("/dev/drbd/by-res/%s/0", resource)
	return fmt.Sprintf(`[Unit]
Description=Mount for %s
[Mount]
What=%s
Where=%s
Type=%s
[Install]
WantedBy=multi-user.target
`, resource, device, mountPoint, fsType)
}

func (rm *ResourceManager) CreateFilesystemOnly(ctx context.Context, resource string, volumeID uint32, fsType string, node string) error {
	// Resolve node to address; empty means the current Primary.
	address, err := rm.resolveNodeOrPrimary(ctx, resource, node)
	if err != nil {
		return fmt.Errorf("resolve target node: %w", err)
	}

	rm.controller.logger.Info("Creating filesystem",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.String("fstype", fsType),
		zap.String("node", node),
		zap.String("address", address))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// Determine DRBD device path
	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	// Create filesystem on the specified node (should be Primary)
	// Note: xfs/btrfs use -f (lowercase), ext4 uses -F (uppercase)
	fsType = strings.ToLower(strings.TrimSpace(fsType))
	forceFlag := "-F"
	if fsType == "xfs" || fsType == "btrfs" {
		forceFlag = "-f"
	}
	mkfsCmd := fmt.Sprintf("sudo mkfs.%s %s %s", fsType, forceFlag, drbdDevice)
	result, err := rm.deployment.Exec(ctx, []string{address}, mkfsCmd)
	if err != nil {
		return fmt.Errorf("failed to create filesystem: %w", err)
	}

	if !result.AllSuccess() {
		var errMsg string
		for host, h := range result.Hosts {
			if !h.Success {
				errMsg = fmt.Sprintf("%s: %s", host, h.Output)
				break
			}
		}
		return fmt.Errorf("filesystem creation failed: %s", errMsg)
	}

	return nil
}
