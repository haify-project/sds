package controller

import (
	"context"
	"fmt"
)

// setMaintenance drains (on=true) or undrains a node by its address, in the
// in-memory registry and in the database alike. The registry is what placement
// and tiebreaker selection read, so writing only the database — as drain once
// did — left a drained node taking new replicas until the controller restarted.
func (nm *NodeManager) setMaintenance(ctx context.Context, address string, on bool) error {
	state := NodeStateOnline
	if on {
		state = NodeStateMaintenance
	}

	nm.mu.Lock()
	n := nm.nodes[address]
	if n == nil {
		nm.mu.Unlock()
		return fmt.Errorf("node %s not found", address)
	}
	prev := n.State
	n.State = state
	rec := nodeRecord(n)
	nm.mu.Unlock()

	if nm.controller.db == nil {
		return nil
	}
	if dbNode, err := nm.controller.db.GetNode(ctx, address); err == nil && dbNode != nil {
		dbNode.State = string(state)
		rec = dbNode
	}
	if err := nm.controller.db.SaveNode(ctx, rec); err != nil {
		nm.mu.Lock()
		if cur := nm.nodes[address]; cur != nil {
			cur.State = prev
		}
		nm.mu.Unlock()
		return fmt.Errorf("save node state: %w", err)
	}
	return nil
}

// nodeStateByName returns the registry state of a node named by name,
// hostname or address, and false when the controller does not know it.
func (nm *NodeManager) nodeStateByName(name string) (NodeState, bool) {
	addr := nm.GetNodeAddressByName(name)
	if addr == "" {
		return "", false
	}
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	if n := nm.nodes[addr]; n != nil {
		return n.State, true
	}
	return "", false
}
