package controller

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// DrainNode moves all Primary DRBD resources off the named node to one of
// their other replica nodes, then marks the node as maintenance in the DB.
// It returns the list of resources that were moved.
func (rm *ResourceManager) DrainNode(ctx context.Context, nodeName string) ([]string, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	// Resolve the node to make sure it exists.
	addr := rm.controller.nodes.GetNodeAddressByName(nodeName)
	if addr == "" {
		return nil, fmt.Errorf("node %q is not registered", nodeName)
	}

	allResources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}

	var moved []string
	for _, dbRes := range allResources {
		nodes := strings.Split(dbRes.Nodes, ",")
		isReplica := false
		for _, n := range nodes {
			if strings.TrimSpace(n) == nodeName {
				isReplica = true
				break
			}
		}
		if !isReplica {
			continue
		}

		// Check whether this node is currently Primary via live DRBD status.
		// NodeStates is keyed by address, so compare addr.
		info, err := rm.GetResource(ctx, dbRes.Name)
		if err != nil {
			rm.controller.logger.Warn("drain: failed to get resource info, skipping",
				zap.String("resource", dbRes.Name), zap.Error(err))
			continue
		}
		ns, ok := info.NodeStates[addr]
		if !ok {
			ns, ok = info.NodeStates[nodeName]
		}
		if !ok || ns.Role != "Primary" {
			continue // already Secondary or not connected
		}

		// Find a different replica node to take over as Primary.
		var target string
		for _, n := range nodes {
			n = strings.TrimSpace(n)
			if n != nodeName {
				target = n
				break
			}
		}
		if target == "" {
			return moved, fmt.Errorf("resource %q has no other replica to take over", dbRes.Name)
		}

		rm.controller.logger.Info("drain: moving primary",
			zap.String("resource", dbRes.Name),
			zap.String("from", nodeName), zap.String("to", target))

		if err := rm.SetSecondary(ctx, dbRes.Name, nodeName); err != nil {
			return moved, fmt.Errorf("set secondary for %q on %q: %w", dbRes.Name, nodeName, err)
		}
		if err := rm.SetPrimary(ctx, dbRes.Name, target, false); err != nil {
			return moved, fmt.Errorf("set primary for %q on %q: %w", dbRes.Name, target, err)
		}
		moved = append(moved, dbRes.Name)
	}

	// Mark the node as maintenance in the DB.
	dbNode, err := rm.controller.db.GetNode(ctx, addr)
	if err != nil || dbNode == nil {
		return moved, fmt.Errorf("node %q not found in database", nodeName)
	}
	dbNode.State = string(NodeStateMaintenance)
	if err := rm.controller.db.SaveNode(ctx, dbNode); err != nil {
		return moved, fmt.Errorf("save node state: %w", err)
	}

	return moved, nil
}

// UndrainNode clears the maintenance state on a node, returning it to service.
func (rm *ResourceManager) UndrainNode(ctx context.Context, nodeName string) error {
	if rm.controller.db == nil {
		return fmt.Errorf("database not available")
	}
	addr := rm.controller.nodes.GetNodeAddressByName(nodeName)
	if addr == "" {
		return fmt.Errorf("node %q is not registered", nodeName)
	}
	dbNode, err := rm.controller.db.GetNode(ctx, addr)
	if err != nil || dbNode == nil {
		return fmt.Errorf("node %q not found in database", nodeName)
	}
	dbNode.State = string(NodeStateOnline)
	return rm.controller.db.SaveNode(ctx, dbNode)
}
