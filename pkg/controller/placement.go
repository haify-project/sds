package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// Auto-placement: when a resource is created without an explicit node list, the
// controller picks where its replicas go. This is the SDS equivalent of
// LINSTOR's autoplace. Placement is capacity-first (most free space in the
// target pool wins) and honours a set of label/affinity constraints:
//
//   - replicas-on-different <key>: each replica lands in a distinct value of the
//     node label <key> (fault-domain spread, e.g. rack).
//   - replicas-on-same <key>: every replica shares one value of <key> (e.g. keep
//     all copies in one zone for latency).
//   - do-not-place-with <resource>: never place a replica on a node that already
//     holds a replica of <resource> (cross-resource anti-affinity).
//
// All are composable: multiple on-different / on-same keys and multiple
// do-not-place-with resources apply together.

// placementNode is a candidate node with the free capacity of its copy of the
// target pool and its label set (for evaluating constraints).
type placementNode struct {
	node   string
	freeGB uint64
	labels map[string]string
}

// placementConstraints are the label-based rules a placement must satisfy.
// do-not-place-with is applied by the caller (it removes nodes before solving),
// so it does not appear here.
type placementConstraints struct {
	onDifferent []string // label keys whose values must differ across replicas
	onSame      []string // label keys whose value must be identical across replicas
}

func (c placementConstraints) empty() bool {
	return len(c.onDifferent) == 0 && len(c.onSame) == 0
}

// referencedKeys is the set of label keys the constraints mention. A node must
// carry every one of them (non-empty) to be eligible — you asked to place by
// these labels, so a node lacking one cannot be reasoned about.
func (c placementConstraints) referencedKeys() []string {
	seen := make(map[string]bool)
	var keys []string
	for _, k := range append(append([]string{}, c.onDifferent...), c.onSame...) {
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys
}

// selectConstrained picks `replicas` nodes from `nodes` satisfying the
// constraints, maximising total free space. Approach: keep only nodes carrying
// every referenced label; partition them into groups that agree on all on-same
// keys; within each group greedily take the most-free nodes that stay pairwise
// distinct on every on-different key; return the best group's selection (most
// total free space, deterministic tie-break by node names). Pure and
// side-effect-free (never mutates `nodes`), so it is unit-testable without a
// live cluster.
func selectConstrained(nodes []placementNode, replicas int, c placementConstraints) ([]string, error) {
	if replicas < 1 {
		return nil, fmt.Errorf("replicas must be >= 1, got %d", replicas)
	}

	// Eligibility: every node must carry all referenced label keys.
	keys := c.referencedKeys()
	eligible := make([]placementNode, 0, len(nodes))
	for _, n := range nodes {
		ok := true
		for _, k := range keys {
			if n.labels[k] == "" {
				ok = false
				break
			}
		}
		if ok {
			eligible = append(eligible, n)
		}
	}

	// Partition into on-same groups (one group when no on-same keys).
	groups := make(map[string][]placementNode)
	var groupOrder []string
	for _, n := range eligible {
		gk := onSameKey(n, c.onSame)
		if _, seen := groups[gk]; !seen {
			groupOrder = append(groupOrder, gk)
		}
		groups[gk] = append(groups[gk], n)
	}

	// Solve each group; keep the best full selection across groups.
	var bestNodes []string
	var bestTotal uint64
	for _, gk := range groupOrder {
		picked, total := pickWithinGroup(groups[gk], replicas, c.onDifferent)
		if len(picked) < replicas {
			continue
		}
		if bestNodes == nil || total > bestTotal || (total == bestTotal && lexLess(picked, bestNodes)) {
			bestNodes, bestTotal = picked, total
		}
	}

	if bestNodes == nil {
		return nil, placementError(replicas, c, len(eligible))
	}
	return bestNodes, nil
}

// pickWithinGroup greedily selects up to `replicas` most-free nodes from one
// on-same group such that no two share a value on any on-different key. Returns
// the chosen node names and their total free space.
func pickWithinGroup(group []placementNode, replicas int, onDifferent []string) ([]string, uint64) {
	sorted := append([]placementNode(nil), group...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].freeGB != sorted[j].freeGB {
			return sorted[i].freeGB > sorted[j].freeGB
		}
		return sorted[i].node < sorted[j].node
	})

	used := make(map[string]map[string]bool, len(onDifferent))
	for _, k := range onDifferent {
		used[k] = make(map[string]bool)
	}

	var picked []string
	var total uint64
	for _, n := range sorted {
		collides := false
		for _, k := range onDifferent {
			if used[k][n.labels[k]] {
				collides = true
				break
			}
		}
		if collides {
			continue
		}
		for _, k := range onDifferent {
			used[k][n.labels[k]] = true
		}
		picked = append(picked, n.node)
		total += n.freeGB
		if len(picked) == replicas {
			break
		}
	}
	return picked, total
}

// onSameKey builds a group key from a node's values for the on-same label keys.
// The empty string (no on-same keys) puts every node in one group.
func onSameKey(n placementNode, onSame []string) string {
	if len(onSame) == 0 {
		return ""
	}
	parts := make([]string, len(onSame))
	for i, k := range onSame {
		parts[i] = n.labels[k]
	}
	return strings.Join(parts, "\x1f")
}

// lexLess reports whether a sorts before b lexicographically element-wise, used
// only as a deterministic tie-break between equally-good selections.
func lexLess(a, b []string) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// placementError explains why no selection was found, naming the binding constraint.
func placementError(replicas int, c placementConstraints, eligible int) error {
	switch {
	case len(c.onSame) > 0:
		return fmt.Errorf("no group of nodes sharing %v can host %d replica(s) satisfying on-different %v (%d eligible nodes)", c.onSame, replicas, c.onDifferent, eligible)
	case len(c.onDifferent) > 0:
		return fmt.Errorf("insufficient fault domains for replicas-on-different %v: cannot place %d replica(s) on distinct values (%d eligible nodes)", c.onDifferent, replicas, eligible)
	default:
		return fmt.Errorf("insufficient capacity for auto-placement: need %d node(s), only %d have enough free space", replicas, eligible)
	}
}

// selectPlacementNodes auto-picks `replicas` node names to host a new resource
// whose volumes need sizeGB total on pool. Candidates are the online nodes whose
// copy of pool currently has at least sizeGB free, minus any node holding a
// replica of a do-not-place-with resource. The constraints then shape the
// selection. Used by CreateResource when the caller supplies no explicit nodes.
func (rm *ResourceManager) selectPlacementNodes(ctx context.Context, pool string, sizeGB uint32, replicas int, onDifferent, onSame, doNotPlaceWith []string) ([]string, error) {
	if replicas < 1 {
		return nil, fmt.Errorf("replicas must be >= 1, got %d", replicas)
	}
	pool = normalizeManagedName(pool)

	pools, err := rm.controller.storage.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pools for placement: %w", err)
	}
	nodes, err := rm.controller.nodes.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes for placement: %w", err)
	}

	// Nodes to avoid: every diskful node of each do-not-place-with resource.
	excluded, err := rm.nodesHostingResources(ctx, doNotPlaceWith)
	if err != nil {
		return nil, err
	}

	// Pools report their host as an address; map it back to a node name (raw and
	// resolved), and capture online state + labels by name.
	nameByAddr := make(map[string]string, len(nodes)*2)
	online := make(map[string]bool, len(nodes))
	labelsByName := make(map[string]map[string]string, len(nodes))
	for _, n := range nodes {
		nameByAddr[n.Address] = n.Name
		nameByAddr[rm.controller.ResolveHost(n.Address)] = n.Name
		online[n.Name] = n.State == NodeStateOnline
		labelsByName[n.Name] = n.Labels
	}

	var cands []placementNode
	seen := make(map[string]bool)
	for _, p := range pools {
		if normalizeManagedName(p.Name) != pool {
			continue
		}
		if p.FreeGB < uint64(sizeGB) {
			continue
		}
		name := nameByAddr[p.Node]
		if name == "" {
			name = p.Node // fall back to whatever the pool recorded
		}
		if !online[name] || seen[name] || excluded[name] {
			continue
		}
		seen[name] = true
		cands = append(cands, placementNode{node: name, freeGB: p.FreeGB, labels: labelsByName[name]})
	}

	constraints := placementConstraints{onDifferent: onDifferent, onSame: onSame}
	picked, err := selectConstrained(cands, replicas, constraints)
	if err != nil {
		return nil, fmt.Errorf("%w (pool %q, need %dGB free per replica)", err, pool, sizeGB)
	}
	rm.controller.logger.Info("auto-placed resource replicas",
		zap.Strings("nodes", picked), zap.String("pool", pool),
		zap.Uint32("size_gb", sizeGB), zap.Int("replicas", replicas),
		zap.Strings("on_different", onDifferent), zap.Strings("on_same", onSame),
		zap.Strings("do_not_place_with", doNotPlaceWith))
	return picked, nil
}

// nodesHostingResources returns the set of node names holding a diskful replica
// of any of the named resources, used to enforce do-not-place-with. Unknown
// resource names are reported as an error so a typo does not silently weaken the
// anti-affinity constraint.
func (rm *ResourceManager) nodesHostingResources(ctx context.Context, resources []string) (map[string]bool, error) {
	excluded := make(map[string]bool)
	if len(resources) == 0 || rm.controller.db == nil {
		return excluded, nil
	}
	for _, name := range resources {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		dbRes, err := rm.controller.db.GetResource(ctx, name)
		if err != nil || dbRes == nil {
			return nil, fmt.Errorf("do-not-place-with resource %q not found", name)
		}
		for _, n := range splitCSV(dbRes.Nodes) {
			excluded[n] = true
		}
	}
	return excluded, nil
}
