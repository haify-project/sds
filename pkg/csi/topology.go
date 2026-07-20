package csi

import (
	"fmt"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// managedPoolPrefix mirrors the controller's normalizeManagedName: SDS-managed
// pools are stored with an "sds_" prefix, so a StorageClass pool of "vg0"
// matches the stored pool "sds_vg0".
const managedPoolPrefix = "sds_"

// managedPoolName normalizes a StorageClass pool name the same way the
// controller does, so it compares equal to the names returned by ListPools.
func managedPoolName(pool string) string {
	pool = strings.TrimSpace(pool)
	if pool == "" || strings.HasPrefix(pool, managedPoolPrefix) {
		return pool
	}
	return managedPoolPrefix + pool
}

// nodesWithPool returns the names of nodes that host the given pool, preserving
// the order of nodes. A node hosts the pool when a PoolInfo with the matching
// (managed) name reports that node — matched by address or name, since the
// controller reports pools keyed by node address. This keeps replica placement
// pool-aware so volumes never land on a node lacking the backing pool.
func nodesWithPool(nodes []*sdspb.NodeInfo, pools []*sdspb.PoolInfo, pool string) []string {
	want := managedPoolName(pool)
	hasPool := map[string]bool{}
	for _, p := range pools {
		if p.GetName() == want && p.GetNode() != "" {
			hasPool[p.GetNode()] = true
		}
	}
	var out []string
	for _, n := range nodes {
		if hasPool[n.GetAddress()] || hasPool[n.GetName()] {
			out = append(out, n.GetName())
		}
	}
	return out
}

// requisiteNodes extracts node names from a CSI topology requirement, preferring
// Preferred order then Requisite. Returns nil when there is no constraint.
func requisiteNodes(req *csi.TopologyRequirement) []string {
	if req == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(tops []*csi.Topology) {
		for _, t := range tops {
			if n := t.GetSegments()[TopologyKeyNode]; n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	add(req.GetPreferred())
	add(req.GetRequisite())
	return out
}

// selectReplicaNodes picks `replicas` node names from available, putting any
// requisite nodes (the scheduler's chosen node) first so a replica lands there.
func selectReplicaNodes(available, requisite []string, replicas int) ([]string, error) {
	avail := map[string]bool{}
	for _, n := range available {
		avail[n] = true
	}
	var picked []string
	used := map[string]bool{}
	for _, n := range requisite {
		if avail[n] && !used[n] {
			picked = append(picked, n)
			used[n] = true
		}
	}
	for _, n := range available {
		if len(picked) >= replicas {
			break
		}
		if !used[n] {
			picked = append(picked, n)
			used[n] = true
		}
	}
	if len(picked) < replicas {
		return nil, fmt.Errorf("need %d replica nodes, only %d available", replicas, len(picked))
	}
	return picked[:replicas], nil
}

// accessibleTopology builds one CSI topology segment per replica node.
func accessibleTopology(nodes []string) []*csi.Topology {
	out := make([]*csi.Topology, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, &csi.Topology{Segments: map[string]string{TopologyKeyNode: n}})
	}
	return out
}
