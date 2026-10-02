package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// The target and the block devices it exports, edited on a gateway that already
// exists.
//
// Creating an iSCSI gateway writes the promoter config once, from a template.
// Everything here does the opposite: it reads that config back, edits one line,
// and writes it out again — because targetcli state is owned by the OCF agents
// and is rebuilt from the config on every promotion, so the config file is the
// only durable place a LUN can be added or removed. The parse/build helpers
// that make a line editable both ways live in config_helpers.go.
//
// The subtlety these functions exist to contain is placement: a LUN line has to
// be inside the promoter's start array and after the target that owns it, so
// insertion is anchored on a landmark (see AddLUN) rather than appended. An
// appended LUN parses fine, distributes fine, and is simply never started.

// AddLUN adds a LUN to an existing iSCSI gateway
func (i *iSCSIManager) AddLUN(ctx context.Context, resource string, lunNumber int, device string) error {
	i.logger.Info("Adding LUN to iSCSI gateway",
		zap.String("resource", resource),
		zap.Int("lun", lunNumber),
		zap.String("device", device))

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return err
	}

	lines, trailingNewline := splitConfigLines(content)
	for _, line := range lines {
		if params, ok := parseISCSILUNLine(line); ok && params["lun"] == fmt.Sprintf("%d", lunNumber) {
			return fmt.Errorf("LUN %d already exists", lunNumber)
		}
	}

	targetIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseISCSITargetLine(line)
		return ok
	})
	if targetIdx < 0 {
		return fmt.Errorf("failed to locate iSCSI target definition")
	}

	targetParams, _ := parseISCSITargetLine(lines[targetIdx])
	iqn := targetParams["iqn"]
	if iqn == "" {
		return fmt.Errorf("failed to parse target IQN from config")
	}

	newLine := buildISCSILUNLine(lunNumber, iqn, device, targetParams["implementation"])
	// After the last LUN (or the target, when there is none): the service IP
	// that follows must stay last in the chain — see the iSCSI template.
	anchor := targetIdx
	for i, line := range lines {
		if _, ok := parseISCSILUNLine(line); ok {
			anchor = i
		}
	}
	lines = append(lines[:anchor+1], append([]string{newLine}, lines[anchor+1:]...)...)

	return i.persistGatewayConfig(ctx, resource, pluginID, joinConfigLines(lines, trailingNewline))
}

// RemoveLUN removes a LUN from an iSCSI gateway
func (i *iSCSIManager) RemoveLUN(ctx context.Context, resource string, lunNumber int) error {
	i.logger.Info("Removing LUN from iSCSI gateway",
		zap.String("resource", resource),
		zap.Int("lun", lunNumber))

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return err
	}

	lines, trailingNewline := splitConfigLines(content)
	lines, removed := removeLine(lines, func(line string) bool {
		params, ok := parseISCSILUNLine(line)
		return ok && params["lun"] == fmt.Sprintf("%d", lunNumber)
	})
	if !removed {
		return fmt.Errorf("LUN %d not found", lunNumber)
	}

	return i.persistGatewayConfig(ctx, resource, pluginID, joinConfigLines(lines, trailingNewline))
}

// ListLUNs lists all configured LUNs for an iSCSI gateway.
func (i *iSCSIManager) ListLUNs(ctx context.Context, resource string) ([]map[string]string, error) {
	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	configPath := gatewayConfigPath(pluginID)
	content, err := i.readGatewayConfig(configPath)
	if err != nil {
		return nil, err
	}

	var luns []map[string]string
	for _, line := range strings.Split(content, "\n") {
		params, ok := parseISCSILUNLine(line)
		if !ok {
			continue
		}
		luns = append(luns, map[string]string{
			"lun":        params["lun"],
			"device":     params["path"],
			"target_iqn": params["target_iqn"],
		})
	}

	return luns, nil
}

// ==================== Target Management ====================

// CreateTarget creates an iSCSI target on the gateway
func (i *iSCSIManager) CreateTarget(ctx context.Context, resource string) error {
	i.logger.Info("Creating iSCSI target", zap.String("resource", resource))

	// Use targetcli or configuration files to create the target
	// This is typically done by the OCF resource agent during start

	return fmt.Errorf("CreateTarget: managed by OCF resource agent")
}

// DeleteTarget deletes an iSCSI target
func (i *iSCSIManager) DeleteTarget(ctx context.Context, resource string) error {
	i.logger.Info("Deleting iSCSI target", zap.String("resource", resource))

	return fmt.Errorf("DeleteTarget: use gateway deletion instead")
}

// ListTargets lists all iSCSI targets
func (i *iSCSIManager) ListTargets(ctx context.Context, host string) ([]string, error) {
	files, err := os.ReadDir(DrbdReactorConfigDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read config directory: %w", err)
	}

	var targets []string
	for _, file := range files {
		if !strings.HasPrefix(file.Name(), "sds-iscsi-") || !strings.HasSuffix(file.Name(), ".toml") {
			continue
		}

		content, err := os.ReadFile(filepath.Join(DrbdReactorConfigDir, file.Name()))
		if err != nil {
			continue
		}

		for _, line := range strings.Split(string(content), "\n") {
			if params, ok := parseISCSITargetLine(line); ok && params["iqn"] != "" {
				targets = append(targets, params["iqn"])
			}
		}
	}

	return uniqueSortedValues(targets), nil
}
