package gateway

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// The NVMe subsystem's host (initiator) allow-list.
//
// The list is one parameter of the nvmet-subsystem line, so both functions here
// rewrite that whole line from its parsed parameters — dropping the serial or
// the NQN while editing the allow-list would change which subsystem clients
// think they are talking to, not just who may talk to it.
//
// As with the iSCSI ACL, an empty allow-list means "allow every host", not
// "deny all". RemoveHost therefore refuses to run against a subsystem that has
// no explicit list yet: the caller is asking to narrow access, and reporting
// success without having narrowed anything is the failure worth preventing.

// ==================== Host Management ====================

// AddHost adds a host (initiator) to the NVMe subsystem
func (n *NVMeManager) AddHost(ctx context.Context, resource, hostNQN string) error {
	n.logger.Info("Adding host to NVMe subsystem",
		zap.String("resource", resource),
		zap.String("host_nqn", hostNQN))

	pluginID := fmt.Sprintf("sds-nvmeof-%s", resource)
	cfg, err := n.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return err
	}
	content := cfg.content

	lines, trailingNewline := splitConfigLines(content)
	subsystemIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseNVMeSubsystemLine(line)
		return ok
	})
	if subsystemIdx < 0 {
		return fmt.Errorf("failed to locate NVMe subsystem definition")
	}

	params, _ := parseNVMeSubsystemLine(lines[subsystemIdx])
	allowed := parseAllowedList(params["allowed_initiators"])
	allowed = append(allowed, hostNQN)
	lines[subsystemIdx] = buildNVMeSubsystemLine(params["nqn"], formatAllowedList(allowed), params["serial"])

	return n.persistGatewayConfig(ctx, resource, pluginID, cfg.disabled, joinConfigLines(lines, trailingNewline))
}

// RemoveHost removes a host from the NVMe subsystem
func (n *NVMeManager) RemoveHost(ctx context.Context, resource, hostNQN string) error {
	n.logger.Info("Removing host from NVMe subsystem",
		zap.String("resource", resource),
		zap.String("host_nqn", hostNQN))

	pluginID := fmt.Sprintf("sds-nvmeof-%s", resource)
	cfg, err := n.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return err
	}
	content := cfg.content

	lines, trailingNewline := splitConfigLines(content)
	subsystemIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseNVMeSubsystemLine(line)
		return ok
	})
	if subsystemIdx < 0 {
		return fmt.Errorf("failed to locate NVMe subsystem definition")
	}

	params, _ := parseNVMeSubsystemLine(lines[subsystemIdx])
	current := parseAllowedList(params["allowed_initiators"])
	if len(current) == 0 {
		return fmt.Errorf("gateway currently allows all initiators; cannot remove a specific host without first defining an explicit allow-list")
	}

	updated := removeValue(current, hostNQN)
	if len(updated) == len(current) {
		return fmt.Errorf("host not found: %s", hostNQN)
	}

	lines[subsystemIdx] = buildNVMeSubsystemLine(params["nqn"], formatAllowedList(updated), params["serial"])
	return n.persistGatewayConfig(ctx, resource, pluginID, cfg.disabled, joinConfigLines(lines, trailingNewline))
}

// ListHosts lists all hosts for an NVMe subsystem
func (n *NVMeManager) ListHosts(ctx context.Context, resource string) ([]string, error) {
	pluginID := fmt.Sprintf("sds-nvmeof-%s", resource)
	cfg, err := n.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return nil, err
	}
	content := cfg.content

	for _, line := range strings.Split(content, "\n") {
		if params, ok := parseNVMeSubsystemLine(line); ok {
			allowed := parseAllowedList(params["allowed_initiators"])
			if len(allowed) == 0 {
				return []string{"ALL"}, nil
			}
			return allowed, nil
		}
	}

	return nil, fmt.Errorf("failed to locate NVMe subsystem definition")
}
