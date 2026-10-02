package gateway

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// Who is allowed to attach to an iSCSI target: the initiator allow-list and the
// CHAP credentials.
//
// These are separated from the LUNs they guard because they are one line in the
// config — the iSCSITarget line — and every function here rewrites that whole
// line from its parsed parameters. Editing it in place instead would be worse
// than verbose: an allow-list update that dropped the portals, or a CHAP update
// that dropped the allow-list, would silently widen access on the next
// promotion. Rebuilding the line from a full parameter set is what makes an
// omission a compile-time argument rather than a security regression.
//
// An empty allow-list is deliberately not "deny all": the iSCSITarget agent
// reads it as "allow every initiator". That inversion is why RemoveInitiator
// refuses to operate on a target that has no ACL yet — the request reads as
// "narrow this down", and silently succeeding against an allow-all target would
// report a tightened ACL that does not exist.

// ==================== ACL Management ====================

// AddInitiator adds an allowed initiator to the iSCSI gateway
func (i *iSCSIManager) AddInitiator(ctx context.Context, resource, initiatorIQN string) error {
	i.logger.Info("Adding initiator ACL",
		zap.String("resource", resource),
		zap.String("iqn", initiatorIQN))

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	cfg, err := i.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return err
	}
	content := cfg.content

	lines, trailingNewline := splitConfigLines(content)
	targetIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseISCSITargetLine(line)
		return ok
	})
	if targetIdx < 0 {
		return fmt.Errorf("failed to locate iSCSI target definition")
	}

	params, _ := parseISCSITargetLine(lines[targetIdx])
	allowed := parseAllowedList(params["allowed_initiators"])
	allowed = append(allowed, initiatorIQN)
	lines[targetIdx] = buildISCSITargetLine(
		params["iqn"],
		params["portals"],
		params["incoming_username"],
		params["incoming_password"],
		formatAllowedList(allowed),
		params["implementation"],
	)

	return i.persistGatewayConfig(ctx, resource, pluginID, cfg.disabled, joinConfigLines(lines, trailingNewline))
}

// RemoveInitiator removes an initiator from the iSCSI gateway
func (i *iSCSIManager) RemoveInitiator(ctx context.Context, resource, initiatorIQN string) error {
	i.logger.Info("Removing initiator ACL",
		zap.String("resource", resource),
		zap.String("iqn", initiatorIQN))

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	cfg, err := i.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return err
	}
	content := cfg.content

	lines, trailingNewline := splitConfigLines(content)
	targetIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseISCSITargetLine(line)
		return ok
	})
	if targetIdx < 0 {
		return fmt.Errorf("failed to locate iSCSI target definition")
	}

	params, _ := parseISCSITargetLine(lines[targetIdx])
	current := parseAllowedList(params["allowed_initiators"])
	if len(current) == 0 {
		return fmt.Errorf("gateway currently allows all initiators; cannot remove a specific initiator without first defining an explicit ACL")
	}

	updated := removeValue(current, initiatorIQN)
	if len(updated) == len(current) {
		return fmt.Errorf("initiator not found: %s", initiatorIQN)
	}

	lines[targetIdx] = buildISCSITargetLine(
		params["iqn"],
		params["portals"],
		params["incoming_username"],
		params["incoming_password"],
		formatAllowedList(updated),
		params["implementation"],
	)

	return i.persistGatewayConfig(ctx, resource, pluginID, cfg.disabled, joinConfigLines(lines, trailingNewline))
}

// ListInitiators lists all initiators for an iSCSI gateway
func (i *iSCSIManager) ListInitiators(ctx context.Context, resource string) ([]string, error) {
	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	cfg, err := i.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return nil, err
	}
	content := cfg.content

	for _, line := range strings.Split(content, "\n") {
		if params, ok := parseISCSITargetLine(line); ok {
			allowed := parseAllowedList(params["allowed_initiators"])
			if len(allowed) == 0 {
				return []string{"ALL"}, nil
			}
			return allowed, nil
		}
	}

	return nil, fmt.Errorf("failed to locate iSCSI target definition")
}

// ==================== CHAP Authentication ====================

// SetCHAP sets CHAP authentication for the iSCSI gateway
func (i *iSCSIManager) SetCHAP(ctx context.Context, resource, username, password string, mutual bool) error {
	i.logger.Info("Setting CHAP authentication",
		zap.String("resource", resource),
		zap.String("username", username),
		zap.Bool("mutual", mutual))

	if mutual {
		return fmt.Errorf("mutual CHAP is not supported by the current iSCSI gateway config writer")
	}

	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	cfg, err := i.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return err
	}
	content := cfg.content

	lines, trailingNewline := splitConfigLines(content)
	targetIdx := findLineIndex(lines, func(line string) bool {
		_, ok := parseISCSITargetLine(line)
		return ok
	})
	if targetIdx < 0 {
		return fmt.Errorf("failed to locate iSCSI target definition")
	}

	params, _ := parseISCSITargetLine(lines[targetIdx])
	lines[targetIdx] = buildISCSITargetLine(
		params["iqn"],
		params["portals"],
		username,
		password,
		formatAllowedList(parseAllowedList(params["allowed_initiators"])),
		params["implementation"],
	)

	return i.persistGatewayConfig(ctx, resource, pluginID, cfg.disabled, joinConfigLines(lines, trailingNewline))
}

// GetCHAP gets CHAP authentication settings
func (i *iSCSIManager) GetCHAP(ctx context.Context, resource string) (username, password string, mutual bool, err error) {
	pluginID := fmt.Sprintf("sds-iscsi-%s", resource)
	cfg, err := i.readGatewayConfig(ctx, resource, pluginID)
	if err != nil {
		return "", "", false, err
	}
	content := cfg.content

	for _, line := range strings.Split(content, "\n") {
		if params, ok := parseISCSITargetLine(line); ok {
			return params["incoming_username"], params["incoming_password"], false, nil
		}
	}

	return "", "", false, fmt.Errorf("failed to locate iSCSI target definition")
}
