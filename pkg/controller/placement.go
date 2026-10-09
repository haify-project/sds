package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// Auto-placement: when a resource is created without an explicit node list, the
// controller picks where its replicas go. This is the Haify equivalent of
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
	node string
	// freeGB only ranks candidates; whether a node may host the volume at all
	// was already decided by poolCapacity.admits before the node got here. Zero
	// is therefore not a rejection: it is a pool that could not report its
	// capacity, or a thin pool with little left that can still take writes, and
	// either one simply sorts behind every node able to prove it has room.
	freeGB uint64
	labels map[string]string
	// thin marks an over-provisioned pool, where freeGB does not cap a volume.
	thin bool
	// freeBytes is the exact figure behind freeGB, which is rounded to the
	// nearest GiB — fine for ranking, wrong for "the largest volume that fits".
	freeBytes uint64
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

// poolCapacity is what one node's copy of a pool can say about its room for a
// new replica, plus how far that answer can be trusted.
//
// Three states, deliberately not two. "No room" and "could not say" are the
// same number — zero — and reading the second as the first is what made
// auto-placement impossible on thin storage: a thin pool is built from nearly
// all of its volume group (see PoolInfo.ThinUsage), so the group reports
// vg_free near zero for the pool's whole life however empty it is. Every
// thin node was therefore filtered out as full, and the failure was reported as
// "insufficient capacity" — sending the operator to hunt for space that was
// never missing. An unknown capacity now costs a node its rank, never its
// candidacy.
type poolCapacity struct {
	// freeGB is what the pool has left, for ranking. Meaningless unless known.
	freeGB uint64
	// freeBytes is the same, unrounded.
	freeBytes uint64
	// known is false when nothing in the pool report describes usable room.
	known bool
	// thin marks an over-provisioned pool, where freeGB is a health signal
	// rather than a ceiling on the size of the next volume.
	thin bool
	// full is LVM's or the thresholds' verdict that a thin pool is out of room.
	full bool
	// sizeBytes and virtualBytes are a thin pool's real and promised size;
	// maxOvercommit, when set, caps their ratio (quota.go).
	sizeBytes, virtualBytes uint64
	maxOvercommit           float64
}

// admits reports whether a pool may host a replica of a volume asking sizeGB.
//
// The rule differs by pool kind, because "free space" means different things:
//
//   - Unknown capacity is admitted. A pool that did not tell us how full it is
//     has not told us it is full, and a filter that treats silence as rejection
//     empties itself of candidates on a cluster that is perfectly healthy. Such
//     a node carries freeGB 0, so it is picked only when nothing better exists.
//
//   - A thick volume group is a hard ceiling: lvcreate fails the instant the
//     group cannot cover the volume, so free >= requested is the real
//     precondition and stays exactly as it always was.
//
//   - A thin pool is not a ceiling, and imposing one here would disable the
//     feature. Thin volumes allocate as they are written, so a 100GB volume on
//     a pool with 10GB left is not an accident — over-provisioning is the
//     reason to run thin at all, and demanding the nominal size up front would
//     refuse every placement that thin exists to allow. What is still worth
//     refusing is a pool with no room for the data it already holds: at
//     ThinPoolFullPercent, or with LVM's own out-of-space flag set, seeding a
//     replica there is how a pool gets pushed over the edge — a new replica
//     syncs, and a resync reallocates every block of a volume (see poolthin.go)
//     — and a pool that refuses writes takes its DRBD disk down with it. Below
//     that line the pool is admitted and ranked by what it has left, so
//     placement still prefers the emptiest node.
func (c poolCapacity) admits(sizeGB uint64) bool {
	switch {
	case !c.known:
		return true
	case c.thin:
		return !c.full && !c.overcommitted(sizeGB)
	default:
		return c.freeGB >= sizeGB
	}
}

// poolPlacementCapacity reads a pool report the way placement needs it.
//
// recordedThin is the pool type the controller has on file. It is consulted
// only when the pool reported no thin utilisation, and only to tell a thin pool
// whose `lvs` read failed (capacity unknown) from a thick group that is
// genuinely full (capacity zero) — two states the numbers alone cannot
// separate, since both read zero free. The live report wins whenever it exists,
// because the record goes stale: a group converted to thin after creation still
// has "vg" on file.
func poolPlacementCapacity(p *PoolInfo, recordedThin bool) poolCapacity {
	if p == nil {
		return poolCapacity{}
	}
	if u := p.ThinUsage; u != nil {
		// SizeBytes zero means the report reached us without the one figure the
		// percentages are a percentage of, which leaves the pool's room unknown
		// rather than exhausted.
		if u.SizeBytes == 0 {
			return poolCapacity{thin: true}
		}
		used := u.DataPercent
		if used < 0 {
			used = 0
		}
		if used > 100 {
			used = 100
		}
		free := float64(u.SizeBytes) * (100 - used) / 100
		return poolCapacity{
			freeGB:       bytesToGB(uint64(free)),
			freeBytes:    uint64(free),
			known:        true,
			thin:         true,
			full:         thinPoolExhausted(u),
			sizeBytes:    u.SizeBytes,
			virtualBytes: u.VirtualBytes,
		}
	}
	if p.Thin || recordedThin {
		return poolCapacity{thin: true}
	}
	return poolCapacity{freeGB: p.FreeGB, freeBytes: p.FreeBytes, known: true}
}

// thinPoolExhausted reports whether a thin pool is too full to be seeded with
// another replica. Metadata counts as well as data because exhausting either
// one stops writes just as completely, and they fill at unrelated rates.
func thinPoolExhausted(u *PoolThinInfo) bool {
	return u.OutOfSpace || u.DataPercent >= ThinPoolFullPercent || u.MetaPercent >= ThinPoolFullPercent
}

// selectPlacementNodes auto-picks `replicas` node names to host a new resource
// whose volumes need sizeGB total on pool. Candidates are the online nodes
// whose copy of pool can take the volume (see poolCapacity.admits), minus any
// node holding a replica of a do-not-place-with resource. The constraints then
// shape the selection. Used by CreateResource when the caller supplies no
// explicit nodes.
//
// warning is set when the replicas could not be spread across fault domains
// (see placement_domain.go).
func (rm *ResourceManager) selectPlacementNodes(ctx context.Context, pool string, sizeGB uint32, replicas int, onDifferent, onSame, doNotPlaceWith []string) ([]string, string, error) {
	if replicas < 1 {
		return nil, "", fmt.Errorf("replicas must be >= 1, got %d", replicas)
	}
	pool = normalizeManagedName(pool)
	cands, err := rm.placementCandidates(ctx, pool, uint64(sizeGB), doNotPlaceWith)
	if err != nil {
		return nil, "", err
	}

	constraints := placementConstraints{onDifferent: onDifferent, onSame: onSame}
	picked, warning, err := spreadAcrossDomains(cands, replicas, constraints, rm.faultDomainKey())
	if err != nil {
		// "%dGB per replica" rather than "%dGB free per replica": on a thin pool
		// the volume is not required to fit in what is free, so naming free
		// space as the requirement would misdescribe why the placement failed.
		return nil, "", fmt.Errorf("%w (pool %q, %dGB per replica)", err, pool, sizeGB)
	}
	if warning != "" {
		rm.controller.logger.Warn("auto-placed replicas share a fault domain", zap.String("warning", warning))
	}
	rm.controller.logger.Info("auto-placed resource replicas",
		zap.Strings("nodes", picked), zap.String("pool", pool),
		zap.Uint32("size_gb", sizeGB), zap.Int("replicas", replicas),
		zap.Strings("on_different", onDifferent), zap.Strings("on_same", onSame),
		zap.Strings("do_not_place_with", doNotPlaceWith))
	return picked, warning, nil
}

// placementCandidates lists the online nodes whose copy of pool admits a
// volume of sizeGB, minus every node holding a diskful replica of a
// doNotPlaceWith resource. Zero admits any size.
func (rm *ResourceManager) placementCandidates(ctx context.Context, pool string, sizeGB uint64, doNotPlaceWith []string) ([]placementNode, error) {
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

	// One lookup for the pool's recorded type, not one per node: the record is
	// keyed by pool name and every node's copy of a pool shares it.
	recordedThin := rm.poolRecordedThin(ctx, pool)

	var cands []placementNode
	seen := make(map[string]bool)
	for _, p := range pools {
		if normalizeManagedName(p.Name) != pool {
			continue
		}
		capacity := poolPlacementCapacity(p, recordedThin)
		capacity.maxOvercommit = rm.maxOvercommit()
		if !capacity.admits(sizeGB) {
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
		cands = append(cands, placementNode{node: name, freeGB: capacity.freeGB, labels: labelsByName[name],
			thin: capacity.thin, freeBytes: capacity.freeBytes})
	}
	return cands, nil
}

// selectAdditionalReplicas picks count more nodes for a resource that already
// has replicas on existing, keeping the constraints true of the whole set: a
// new node may not repeat an existing replica's value for an onDifferent key,
// and must share the existing replicas' value for an onSame key. Nodes in
// barred are never picked: a resource's tiebreaker or diskless client, which
// AddReplica refuses, for a caller that cannot turn one into a replica.
func (rm *ResourceManager) selectAdditionalReplicas(ctx context.Context, pool string, sizeGB uint64, count int, existing, barred, onDifferent, onSame []string) ([]string, error) {
	pool = normalizeManagedName(pool)
	cands, err := rm.placementCandidates(ctx, pool, sizeGB, nil)
	if err != nil {
		return nil, err
	}
	nodes, err := rm.controller.nodes.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	labelsByName := make(map[string]map[string]string, len(nodes))
	for _, n := range nodes {
		labelsByName[n.Name] = n.Labels
	}
	have := make(map[string]bool, len(existing)+len(barred))
	for _, n := range existing {
		have[n] = true
	}
	for _, n := range barred {
		have[n] = true
	}

	var filtered, apart []placementNode
	key := rm.faultDomainKey()
	usedDomains := map[string]bool{}
	for _, e := range existing {
		usedDomains[faultDomain(e, labelsByName[e], key)] = true
	}
	for _, c := range cands {
		if have[c.node] || !fitsExistingReplicas(c.labels, existing, labelsByName, onDifferent, onSame) {
			continue
		}
		filtered = append(filtered, c)
		if !usedDomains[faultDomain(c.node, c.labels, key)] {
			apart = append(apart, c)
		}
	}
	constraints := placementConstraints{onDifferent: onDifferent, onSame: onSame}
	// A new replica in a fault domain the resource does not use yet is worth
	// more than one with more free space beside an existing copy.
	if picked, warning, err := spreadAcrossDomains(apart, count, constraints, key); err == nil && warning == "" {
		return picked, nil
	}
	picked, err := selectConstrained(filtered, count, constraints)
	if err != nil {
		return nil, fmt.Errorf("%w (pool %q, %d more replica(s) beside %s)", err, pool, count, strings.Join(existing, ", "))
	}
	return picked, nil
}

// fitsExistingReplicas reports whether a node with these labels may join the
// existing replicas under the constraints.
func fitsExistingReplicas(labels map[string]string, existing []string, labelsByName map[string]map[string]string, onDifferent, onSame []string) bool {
	for _, k := range onDifferent {
		for _, e := range existing {
			if v, ok := labelsByName[e][k]; ok && labels[k] == v {
				return false
			}
		}
	}
	for _, k := range onSame {
		for _, e := range existing {
			if v, ok := labelsByName[e][k]; ok && labels[k] != v {
				return false
			}
		}
	}
	return true
}

// poolRecordedThin reports whether the controller created this pool as a thin
// pool. A pool the cluster cannot currently be asked about — an unreachable
// node, a failed `lvs` — still has its type on file, and that is the only thing
// left to distinguish a thin pool of unknown fullness from a full thick group.
// A missing or unreadable record answers "not thin", which is the pre-existing
// behaviour and never widens what placement accepts.
func (rm *ResourceManager) poolRecordedThin(ctx context.Context, pool string) bool {
	if rm.controller.db == nil {
		return false
	}
	p, err := rm.controller.db.GetPool(ctx, pool)
	if err != nil || p == nil {
		return false
	}
	return isThinPoolType(p.Type)
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
