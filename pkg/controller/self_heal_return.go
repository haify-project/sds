package controller

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"
)

// A node coming back.
//
// A replica added while a member was unreachable (add-replica with
// allow_unreachable) left that member with a config that does not know the
// newcomer; the resource recorded it in StaleConfigNodes. When the member
// answers again its configs are repaired — the same `resource repair` an
// operator would run — and it is dropped from the list.

// persistHealth writes a node's state and OfflineSince after a transition.
func (nm *NodeManager) persistHealth(address string) {
	if nm.controller.db == nil {
		return
	}
	nm.mu.RLock()
	n := nm.nodes[address]
	if n == nil {
		nm.mu.RUnlock()
		return
	}
	rec := nodeRecord(n)
	nm.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if existing, err := nm.controller.db.GetNode(ctx, address); err == nil && existing != nil {
		existing.State, existing.OfflineSince, existing.LastSeen = rec.State, rec.OfflineSince, rec.LastSeen
		rec = existing
	}
	if err := nm.controller.db.SaveNode(ctx, rec); err != nil {
		nm.controller.logger.Warn("Failed to persist node health", zap.String("node", address), zap.Error(err))
	}
}

// repairStaleConfigs repairs the configs node missed while it was away.
func (rm *ResourceManager) repairStaleConfigs(node string) {
	if rm == nil || rm.controller.db == nil || node == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	resources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return
	}
	for _, r := range resources {
		stale := splitCSV(r.StaleConfigNodes)
		if !contains(stale, node) {
			continue
		}
		if err := rm.RepairResourceConfig(ctx, r.Name); err != nil {
			rm.controller.logger.Warn("Node is back but its config of the resource could not be repaired; run `haify resource repair`",
				zap.String("node", node), zap.String("resource", r.Name), zap.Error(err))
			continue
		}
		r.StaleConfigNodes = strings.Join(without(stale, node), ",")
		if err := rm.controller.db.SaveResource(ctx, r); err != nil {
			rm.controller.logger.Warn("Repaired, but could not record it", zap.String("resource", r.Name), zap.Error(err))
			continue
		}
		rm.controller.logger.Info("Repaired the config a returning node missed",
			zap.String("node", node), zap.String("resource", r.Name))
	}
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
