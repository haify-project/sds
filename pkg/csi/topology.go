package csi

import (
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

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
