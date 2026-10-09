package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Fault domains. Two replicas on two VMs of the same physical host are one
// failure away from being zero replicas, and a tiebreaker on that host turns
// the same failure into lost quorum for the survivor elsewhere. Haify cannot see
// the host a VM runs on, so the operator says it with a node label — by default
// `host` (`sds node set-labels <node> host=<machine>`), configurable as
// [resource] fault_domain_label.
//
// Automatic placement prefers distinct domains but does not insist: a
// three-VM lab on one machine must still be able to create a resource. When it
// cannot spread, it says so. The explicit replicas-on-different constraint is
// the hard version of the same rule.

// defaultFaultDomainLabel is used when the controller has no configuration.
const defaultFaultDomainLabel = "host"

// faultDomainKey is the node label that names a fault domain.
func (rm *ResourceManager) faultDomainKey() string {
	if rm.controller != nil && rm.controller.config != nil {
		if k := strings.TrimSpace(rm.controller.config.Resource.FaultDomainLabel); k != "" {
			return k
		}
	}
	return defaultFaultDomainLabel
}

// faultDomain names the domain a node belongs to. A node without the label is
// a domain of its own, so an unlabelled cluster places exactly as before.
func faultDomain(node string, labels map[string]string, key string) string {
	if v := labels[key]; v != "" {
		return key + "=" + v
	}
	return "node:" + node
}

// labelledDomain reports whether the domain came from the label rather than
// standing in for a single node.
func labelledDomain(d string) bool {
	return !strings.HasPrefix(d, "node:")
}

// withDomainLabels returns the candidates with key filled in for nodes that
// lack it, each with a value of its own, so key can be used as an on-different
// constraint without excluding unlabelled nodes.
func withDomainLabels(nodes []placementNode, key string) []placementNode {
	out := make([]placementNode, len(nodes))
	for i, n := range nodes {
		labels := make(map[string]string, len(n.labels)+1)
		for k, v := range n.labels {
			labels[k] = v
		}
		labels[key] = strings.TrimPrefix(faultDomain(n.node, n.labels, key), key+"=")
		n.labels = labels
		out[i] = n
	}
	return out
}

// spreadAcrossDomains places replicas under the hard constraints c, preferring
// a selection in which no two replicas share a fault domain. warning is set
// when the preference could not be met.
func spreadAcrossDomains(nodes []placementNode, replicas int, c placementConstraints, key string) ([]string, string, error) {
	spread := c
	if !containsString(c.onDifferent, key) {
		spread.onDifferent = append(append([]string(nil), c.onDifferent...), key)
	}
	if picked, err := selectConstrained(withDomainLabels(nodes, key), replicas, spread); err == nil {
		return picked, "", nil
	}
	picked, err := selectConstrained(nodes, replicas, c)
	if err != nil {
		return nil, "", err
	}
	labels := make(map[string]map[string]string, len(nodes))
	for _, n := range nodes {
		labels[n.node] = n.labels
	}
	return picked, sharedDomainWarning(picked, labels, key), nil
}

// sharedDomainWarning describes replicas that ended up in one fault domain.
func sharedDomainWarning(nodes []string, labels map[string]map[string]string, key string) string {
	byDomain := map[string][]string{}
	for _, n := range nodes {
		d := faultDomain(n, labels[n], key)
		byDomain[d] = append(byDomain[d], n)
	}
	var parts []string
	for d, ns := range byDomain {
		if len(ns) > 1 {
			parts = append(parts, fmt.Sprintf("%s share %s", strings.Join(ns, " and "), d))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return fmt.Sprintf("replicas %s: one failure there loses every copy on it; no candidate set spans more %q values",
		strings.Join(parts, "; "), key)
}

// faultDomainRisk names the fault domain whose loss would take the resource
// down: one holding every diskful replica (the data), or a majority of the
// voters (quorum). Empty when no labelled domain does. Unlabelled nodes are
// their own domains and never trip it, so it only speaks once the operator has
// said which nodes fail together.
func faultDomainRisk(diskful, tiebreakers []string, labels map[string]map[string]string, key string) string {
	if len(diskful) < 2 {
		return ""
	}
	voters := map[string]int{}
	data := map[string]int{}
	for _, n := range diskful {
		d := faultDomain(n, labels[n], key)
		voters[d]++
		data[d]++
	}
	for _, n := range tiebreakers {
		voters[faultDomain(n, labels[n], key)]++
	}
	total := len(diskful) + len(tiebreakers)
	var risky []string
	for d, v := range voters {
		if !labelledDomain(d) {
			continue
		}
		if data[d] == len(diskful) || 2*v >= total {
			risky = append(risky, d)
		}
	}
	sort.Strings(risky)
	return strings.Join(risky, ",")
}

// labelsByNode maps every registered node to its labels.
func (rm *ResourceManager) labelsByNode(ctx context.Context) map[string]map[string]string {
	out := map[string]map[string]string{}
	if rm.controller == nil || rm.controller.nodes == nil {
		return out
	}
	nodes, err := rm.controller.nodes.ListNodes(ctx)
	if err != nil {
		return out
	}
	for _, n := range nodes {
		if n != nil {
			out[n.Name] = n.Labels
		}
	}
	return out
}
