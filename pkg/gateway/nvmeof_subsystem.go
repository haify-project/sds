package gateway

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// The subsystem's exported objects — namespaces and the transport port —
// managed on a gateway that already exists.
//
// Like the iSCSI target file, these functions edit the promoter config rather
// than the live target: the nvmet configfs tree under /sys/kernel/config/nvmet
// is torn down and rebuilt from the config on every promotion, so a namespace
// added directly to a running node disappears at the next failover. The config
// file is the only state that survives.
//
// Namespace IDs are allocated by scanning the existing lines for the current
// maximum rather than by counting them. Reusing the ID of a removed namespace
// would let a client that still has the old one open bind to different backing
// storage under the same identifier — which is the whole reason a namespace ID
// is meant to be stable for the life of the subsystem.
//
// Ports sit here rather than with the host allow-list because a port is not
// access control: it is where the subsystem is reachable, i.e. part of what the
// subsystem exports, and its line is anchored relative to the namespace lines
// that must already exist when nvmet-port starts.

// ==================== Namespace Management ====================

// AddNamespace adds a namespace to an existing NVMe-oF gateway
func (n *NVMeManager) AddNamespace(ctx context.Context, resource, device string) error {
	n.logger.Info("Adding namespace to NVMe-oF gateway",
		zap.String("resource", resource),
		zap.String("device", device))

	pluginID := fmt.Sprintf("sds-nvmeof-%s", resource)
	cfg, err := n.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return err
	}
	content := cfg.content

	lines, trailingNewline := splitConfigLines(content)
	maxNamespaceID := 0
	for _, line := range lines {
		params, ok := parseNVMeNamespaceLine(line)
		if !ok {
			continue
		}
		nsid, err := parseIntParam(params, "namespace_id")
		if err == nil && nsid > maxNamespaceID {
			maxNamespaceID = nsid
		}
		if params["backing_path"] == device {
			return fmt.Errorf("namespace for device already exists: %s", device)
		}
	}

	subsystemIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseNVMeSubsystemLine(line)
		return ok
	})
	if subsystemIdx < 0 {
		return fmt.Errorf("failed to locate NVMe subsystem definition")
	}

	subsystemParams, _ := parseNVMeSubsystemLine(lines[subsystemIdx])
	nqn := subsystemParams["nqn"]
	if nqn == "" {
		return fmt.Errorf("failed to parse NVMe subsystem NQN from config")
	}

	newLine := buildNVMeNamespaceLine(maxNamespaceID+1, nqn, device)
	lines, err = insertLineBefore(lines, newLine, func(line string) bool {
		return strings.Contains(line, "ocf:heartbeat:nvmet-port ")
	})
	if err != nil {
		return err
	}

	return n.persistGatewayConfig(ctx, resource, pluginID, cfg.disabled, joinConfigLines(lines, trailingNewline))
}

// RemoveNamespace removes a namespace from an NVMe-oF gateway
func (n *NVMeManager) RemoveNamespace(ctx context.Context, resource string, nsid int) error {
	n.logger.Info("Removing namespace from NVMe-oF gateway",
		zap.String("resource", resource),
		zap.Int("nsid", nsid))

	pluginID := fmt.Sprintf("sds-nvmeof-%s", resource)
	cfg, err := n.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return err
	}
	content := cfg.content

	lines, trailingNewline := splitConfigLines(content)
	lines, removed := removeLine(lines, func(line string) bool {
		params, ok := parseNVMeNamespaceLine(line)
		return ok && params["namespace_id"] == fmt.Sprintf("%d", nsid)
	})
	if !removed {
		return fmt.Errorf("namespace %d not found", nsid)
	}

	return n.persistGatewayConfig(ctx, resource, pluginID, cfg.disabled, joinConfigLines(lines, trailingNewline))
}

// ListNamespaces lists all namespaces for an NVMe-oF gateway
func (n *NVMeManager) ListNamespaces(ctx context.Context, resource string) ([]map[string]string, error) {
	pluginID := fmt.Sprintf("sds-nvmeof-%s", resource)
	cfg, err := n.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return nil, err
	}
	content := cfg.content

	var namespaces []map[string]string
	for _, line := range strings.Split(content, "\n") {
		params, ok := parseNVMeNamespaceLine(line)
		if !ok {
			continue
		}
		namespaces = append(namespaces, map[string]string{
			"namespace_id": params["namespace_id"],
			"backing_path": params["backing_path"],
			"uuid":         params["uuid"],
			"nguid":        params["nguid"],
			"nqn":          params["nqn"],
		})
	}

	return namespaces, nil
}

// ==================== Subsystem Management ====================

// CreateSubsystem creates an NVMe subsystem
func (n *NVMeManager) CreateSubsystem(ctx context.Context, resource, nqn string) error {
	n.logger.Info("Creating NVMe subsystem",
		zap.String("resource", resource),
		zap.String("nqn", nqn))

	// Use nvmetcli or configuration files to create the subsystem
	return fmt.Errorf("CreateSubsystem: managed by OCF resource agent")
}

// DeleteSubsystem deletes an NVMe subsystem
func (n *NVMeManager) DeleteSubsystem(ctx context.Context, nqn string) error {
	n.logger.Info("Deleting NVMe subsystem", zap.String("nqn", nqn))

	return fmt.Errorf("DeleteSubsystem: use gateway deletion instead")
}

// ListSubsystems lists the NQNs of the running NVMe-oF gateways' subsystems,
// read from the managed nodes like ListTargets.
func (n *NVMeManager) ListSubsystems(ctx context.Context, host string) ([]string, error) {
	contents, err := n.readAllNodeConfigs(ctx, "sds-nvmeof-*.toml")
	if err != nil {
		return nil, err
	}

	var subsystems []string
	for _, content := range contents {
		for _, line := range strings.Split(content, "\n") {
			if params, ok := parseNVMeSubsystemLine(line); ok && params["nqn"] != "" {
				subsystems = append(subsystems, params["nqn"])
			}
		}
	}

	return uniqueSortedValues(subsystems), nil
}

// ==================== Port Management ====================

// CreatePort creates an NVMe port (transport endpoint)
func (n *NVMeManager) CreatePort(ctx context.Context, resource, addr string, port int) error {
	n.logger.Info("Creating NVMe port",
		zap.String("resource", resource),
		zap.String("addr", addr),
		zap.Int("port", port))

	return fmt.Errorf("CreatePort: managed by OCF resource agent")
}

// DeletePort deletes an NVMe port
func (n *NVMeManager) DeletePort(ctx context.Context, resource, addr string, port int) error {
	n.logger.Info("Deleting NVMe port",
		zap.String("resource", resource),
		zap.String("addr", addr),
		zap.Int("port", port))

	if port == 0 {
		port = DefaultNVMePort
	}
	if port != DefaultNVMePort {
		return fmt.Errorf("custom NVMe port removal is not supported by the current config writer")
	}

	pluginID := fmt.Sprintf("sds-nvmeof-%s", resource)
	cfg, err := n.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return err
	}
	content := cfg.content

	lines, trailingNewline := splitConfigLines(content)
	lines, removed := removeLine(lines, func(line string) bool {
		params, ok := parseNVMePortLine(line)
		return ok && params["addr"] == addr
	})
	if !removed {
		return fmt.Errorf("port not found for addr %s", addr)
	}

	return n.persistGatewayConfig(ctx, resource, pluginID, cfg.disabled, joinConfigLines(lines, trailingNewline))
}

// ListPorts lists all ports for an NVMe subsystem
func (n *NVMeManager) ListPorts(ctx context.Context, resource string) ([]map[string]string, error) {
	pluginID := fmt.Sprintf("sds-nvmeof-%s", resource)
	cfg, err := n.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return nil, err
	}
	content := cfg.content

	var ports []map[string]string
	for _, line := range strings.Split(content, "\n") {
		params, ok := parseNVMePortLine(line)
		if !ok {
			continue
		}
		ports = append(ports, map[string]string{
			"addr": params["addr"],
			"type": params["type"],
			"nqns": params["nqns"],
			"port": fmt.Sprintf("%d", DefaultNVMePort),
		})
	}

	return ports, nil
}
