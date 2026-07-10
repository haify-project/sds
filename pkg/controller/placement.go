package controller

import (
	"context"
	"fmt"
	"sort"

	"go.uber.org/zap"
)

// Auto-placement: when a resource is created without an explicit node list, the
// controller picks where its replicas go. This is the SDS equivalent of
// LINSTOR's autoplace. The MVP is a greedy capacity-first placer — it puts one
// replica each on the nodes whose target pool has the most free space. It does
// NOT yet model fault domains (rack/zone spread) or do-not-place-with
// constraints; "one replica per node" is the only spread it guarantees.

// placementCandidate is a node that can host a replica, with the free capacity
// of its copy of the target pool and its fault domain (the value of the
// constraint label, e.g. rack="A"; empty when the node is unlabeled).
type placementCandidate struct {
	node   string
	freeGB uint64
	domain string
}

// pickNodesByFreeSpace selects `replicas` distinct nodes from cands, most-free
// first, breaking ties by node name so placement is deterministic. It errors if
// fewer than `replicas` candidates are available. Pure and side-effect-free so
// the selection policy is unit-testable without a live cluster.
func pickNodesByFreeSpace(cands []placementCandidate, replicas int) ([]string, error) {
	if replicas < 1 {
		return nil, fmt.Errorf("replicas must be >= 1, got %d", replicas)
	}
	sorted := append([]placementCandidate(nil), cands...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].freeGB != sorted[j].freeGB {
			return sorted[i].freeGB > sorted[j].freeGB
		}
		return sorted[i].node < sorted[j].node
	})
	if len(sorted) < replicas {
		return nil, fmt.Errorf("insufficient capacity for auto-placement: need %d node(s), only %d have enough free space", replicas, len(sorted))
	}
	picked := make([]string, replicas)
	for i := 0; i < replicas; i++ {
		picked[i] = sorted[i].node
	}
	return picked, nil
}

// pickNodesOnDifferentDomains selects `replicas` nodes that all sit in DISTINCT
// fault domains, so a single domain (rack/zone) failure never takes out two
// replicas. It keeps the most-free node in each domain, then picks the
// `replicas` domains whose best node has the most free space. Candidates with
// an empty domain are ineligible (an unlabeled node has no known domain to
// spread across). Deterministic: ties break by node name. Pure/testable.
func pickNodesOnDifferentDomains(cands []placementCandidate, replicas int) ([]string, error) {
	if replicas < 1 {
		return nil, fmt.Errorf("replicas must be >= 1, got %d", replicas)
	}

	// Best (most-free, then name) candidate per domain.
	best := make(map[string]placementCandidate)
	for _, c := range cands {
		if c.domain == "" {
			continue // unlabeled: cannot participate in domain spreading
		}
		cur, ok := best[c.domain]
		if !ok || c.freeGB > cur.freeGB || (c.freeGB == cur.freeGB && c.node < cur.node) {
			best[c.domain] = c
		}
	}

	reps := make([]placementCandidate, 0, len(best))
	for _, c := range best {
		reps = append(reps, c)
	}
	sort.Slice(reps, func(i, j int) bool {
		if reps[i].freeGB != reps[j].freeGB {
			return reps[i].freeGB > reps[j].freeGB
		}
		return reps[i].node < reps[j].node
	})

	if len(reps) < replicas {
		return nil, fmt.Errorf("insufficient fault domains for replicas-on-different: need %d distinct domains, found %d", replicas, len(reps))
	}
	picked := make([]string, replicas)
	for i := 0; i < replicas; i++ {
		picked[i] = reps[i].node
	}
	return picked, nil
}

// selectPlacementNodes auto-picks `replicas` node names to host a new resource
// whose volumes need sizeGB total on pool. Candidates are the online nodes whose
// copy of pool currently has at least sizeGB free. When onDifferentKey is set,
// each replica lands in a distinct value of that node label (fault-domain
// spread); otherwise the most-free nodes win. Used by CreateResource when the
// caller supplies no explicit node list.
func (rm *ResourceManager) selectPlacementNodes(ctx context.Context, pool string, sizeGB uint32, replicas int, onDifferentKey string) ([]string, error) {
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

	// Pools report their host as an address; map it back to a node name (both
	// the raw and resolved forms), and capture online state + labels by name.
	nameByAddr := make(map[string]string, len(nodes)*2)
	online := make(map[string]bool, len(nodes))
	labelsByName := make(map[string]map[string]string, len(nodes))
	for _, n := range nodes {
		nameByAddr[n.Address] = n.Name
		nameByAddr[rm.controller.ResolveHost(n.Address)] = n.Name
		online[n.Name] = n.State == NodeStateOnline
		labelsByName[n.Name] = n.Labels
	}

	var cands []placementCandidate
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
		if !online[name] || seen[name] {
			continue
		}
		seen[name] = true
		domain := ""
		if onDifferentKey != "" {
			domain = labelsByName[name][onDifferentKey]
		}
		cands = append(cands, placementCandidate{node: name, freeGB: p.FreeGB, domain: domain})
	}

	var picked []string
	if onDifferentKey != "" {
		picked, err = pickNodesOnDifferentDomains(cands, replicas)
	} else {
		picked, err = pickNodesByFreeSpace(cands, replicas)
	}
	if err != nil {
		return nil, fmt.Errorf("%w (pool %q, need %dGB free per replica)", err, pool, sizeGB)
	}
	rm.controller.logger.Info("auto-placed resource replicas",
		zap.Strings("nodes", picked), zap.String("pool", pool),
		zap.Uint32("size_gb", sizeGB), zap.Int("replicas", replicas),
		zap.String("on_different", onDifferentKey))
	return picked, nil
}
