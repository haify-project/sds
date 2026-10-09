package controller

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/event"
)

// Rebalancing, by plan.
//
// Nothing rebalances on its own: a move is a full sync of the replica (1 TB is
// tens of minutes on 10GbE, hours on 1GbE) and the right moment is an
// operator's call. The planner proposes moves from the node holding the most
// allocated capacity to nodes that can take a replica and hold less, until the
// spread is within a tenth of the mean; applying the plan runs the moves one
// after another (move-replica: the new replica syncs before the old one goes).
//
// Left out, with a note: the controller's metadata, WAN resources, resources
// already moving, and CSI volumes — a PersistentVolume's node affinity is
// fixed when it is created, so moving its replica limits where its pod can be
// scheduled.
//
// Allocated capacity is what a node has promised, not what it has written: on
// thin pools the two part ways, and the node holding the least by allocation
// can be the one whose pool is nearly full. A move therefore never goes to a
// node whose pool is already fuller, in what is really written, than the
// source's.

// RebalanceMove is one proposed move.
type RebalanceMove struct {
	Resource string
	From, To string
	SizeGB   uint64
}

// PlanRebalance proposes at most maxMoves moves, with notes on what it left out.
func (rm *ResourceManager) PlanRebalance(ctx context.Context, maxMoves int) ([]RebalanceMove, []string, error) {
	if maxMoves <= 0 {
		maxMoves = 10
	}
	nodes, err := rm.controller.nodes.ListNodes(ctx)
	if err != nil {
		return nil, nil, err
	}
	alloc := map[string]uint64{}
	for _, n := range nodes {
		if n.State == NodeStateOnline {
			alloc[n.Name] = 0
		}
	}
	resources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return nil, nil, err
	}
	type held struct {
		name    string
		members []string
		barred  []string
		pool    string
		size    uint64
	}
	var all []held
	var notes []string
	for _, r := range resources {
		pool, size := rm.memberPoolAndSize(ctx, r.Name, "")
		members := splitCSV(r.Nodes)
		if r.WANMode {
			members = without(members, r.DRNode)
		}
		for _, m := range members {
			if _, ok := alloc[m]; ok {
				alloc[m] += size
			}
		}
		switch {
		case r.Name == SelfHaResource, r.WANMode, r.MoveFrom != "":
			continue
		case externallyPromoted(r.Name, r.Labels) == "the CSI driver":
			notes = append(notes, r.Name+": a CSI volume, whose node affinity is fixed; move it by hand if you must")
			continue
		}
		all = append(all, held{r.Name, members, nonReplicaMembers(r), pool, size})
	}
	if len(alloc) < 2 {
		return nil, notes, nil
	}
	fill := rm.poolFillByNode(ctx)
	sort.Slice(all, func(i, j int) bool { return all[i].size < all[j].size })

	var moves []RebalanceMove
	moved := map[string]bool{}
	for len(moves) < maxMoves {
		from, low := spread(alloc)
		var total uint64
		for _, v := range alloc {
			total += v
		}
		mean := total / uint64(len(alloc))
		if alloc[from]-alloc[low] <= max(mean/10, 1) {
			break
		}
		found := false
		for _, h := range all {
			if moved[h.name] || !contains(h.members, from) || h.size == 0 {
				continue
			}
			picked, err := rm.selectAdditionalReplicas(ctx, h.pool, h.size, 1, h.members, h.barred, nil, nil)
			if err != nil || len(picked) == 0 {
				continue
			}
			to := picked[0]
			if _, ok := alloc[to]; !ok || alloc[to]+h.size >= alloc[from] {
				continue
			}
			if fuller(fill[h.pool], to, from) {
				continue
			}
			moves = append(moves, RebalanceMove{Resource: h.name, From: from, To: to, SizeGB: h.size})
			alloc[from] -= h.size
			alloc[to] += h.size
			moved[h.name] = true
			found = true
			break
		}
		if !found {
			break
		}
	}
	return moves, notes, nil
}

// nonReplicaMembers are r's tiebreakers and diskless clients: members that
// AddReplica and MoveReplica refuse as a target.
func nonReplicaMembers(r *database.Resource) []string {
	return append(splitCSV(r.DisklessNodes), splitCSV(r.DisklessClients)...)
}

// poolFillByNode is, per pool and node name, the fraction of the pool really
// in use: written data on a thin pool, allocated extents on a thick one.
func (rm *ResourceManager) poolFillByNode(ctx context.Context) map[string]map[string]float64 {
	fill := map[string]map[string]float64{}
	pools, err := rm.controller.storage.ListPools(ctx)
	if err != nil {
		return fill
	}
	nodes, err := rm.controller.nodes.ListNodes(ctx)
	if err != nil {
		return fill
	}
	nameByAddr := make(map[string]string, len(nodes)*2)
	for _, n := range nodes {
		nameByAddr[n.Address] = n.Name
		nameByAddr[rm.controller.ResolveHost(n.Address)] = n.Name
	}
	for _, p := range pools {
		f, ok := poolFill(p)
		if !ok {
			continue
		}
		name := nameByAddr[p.Node]
		if name == "" {
			name = p.Node
		}
		pool := normalizeManagedName(p.Name)
		if fill[pool] == nil {
			fill[pool] = map[string]float64{}
		}
		fill[pool][name] = f
	}
	return fill
}

// poolFill is the fraction of p in use, and whether p reported it.
func poolFill(p *PoolInfo) (float64, bool) {
	if p == nil {
		return 0, false
	}
	if u := p.ThinUsage; u != nil {
		if u.SizeBytes == 0 {
			return 0, false
		}
		return min(max(u.DataPercent, 0), 100) / 100, true
	}
	if p.TotalBytes == 0 || p.FreeBytes > p.TotalBytes {
		return 0, false
	}
	return float64(p.TotalBytes-p.FreeBytes) / float64(p.TotalBytes), true
}

// fuller reports whether to's pool is at least as full as from's; a node
// whose fill is not known is not held against.
func fuller(fill map[string]float64, to, from string) bool {
	t, okTo := fill[to]
	f, okFrom := fill[from]
	return okTo && okFrom && t >= f
}

// spread returns the most and the least allocated node.
func spread(alloc map[string]uint64) (string, string) {
	var hi, lo string
	for n, v := range alloc {
		if hi == "" || v > alloc[hi] || (v == alloc[hi] && n < hi) {
			hi = n
		}
		if lo == "" || v < alloc[lo] || (v == alloc[lo] && n < lo) {
			lo = n
		}
	}
	return hi, lo
}

var rebalanceMu sync.Mutex

// ApplyRebalance runs moves one after another in the background; it refuses
// while a previous plan is still running.
func (rm *ResourceManager) ApplyRebalance(moves []RebalanceMove) error {
	if !rebalanceMu.TryLock() {
		return fmt.Errorf("a rebalance is already running")
	}
	go func() {
		defer rebalanceMu.Unlock()
		ctx := rm.controller.ctx
		for _, m := range moves {
			if err := rm.MoveReplica(ctx, m.Resource, m.From, m.To); err != nil {
				rm.moveEvent(m.Resource, event.SeverityWarning, fmt.Sprintf(
					"rebalance: moving %s's replica from %s to %s was refused: %v", m.Resource, m.From, m.To, err))
				continue
			}
			// One sync at a time: wait for this move to finish.
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(movePollInterval):
				}
				if r, err := rm.controller.db.GetResource(ctx, m.Resource); err != nil || r == nil || r.MoveFrom == "" {
					break
				}
			}
		}
	}()
	return nil
}
