package inspect

import (
	"fmt"
	"sort"
	"strings"
)

// disconnectedPeers reports each peer of r that some node cannot reach, once
// per peer, naming every node that cannot reach it. A powered-off client used
// to produce one line per (node, peer) pair, each suggesting the same command
// on the wrong side.
//
// StandAlone connections are judged per side by replicaChecks: there the side
// matters, because only that node can reconnect.
func disconnectedPeers(in *Input, r Resource) []Check {
	from := map[string][]string{}
	for _, n := range participants(r) {
		p, ok := in.probe(n)
		if !ok || !p.DRBDOK {
			continue
		}
		v := p.Resource(r.Name)
		if v == nil {
			continue
		}
		for _, c := range v.Connections {
			if c.Connected() || strings.EqualFold(c.ConnectionState, "StandAlone") {
				continue
			}
			peer := in.nodeName(c.Name)
			from[peer] = append(from[peer], fmt.Sprintf("%s: %s", in.nodeName(n), c.ConnectionState))
		}
	}
	var out []Check
	for _, peer := range sortedKeys(from) {
		ev := from[peer]
		sort.Strings(ev)
		var who []string
		for _, e := range ev {
			who = append(who, strings.SplitN(e, ":", 2)[0])
		}
		c := Check{ID: "resource.disconnected", Area: AreaResources, Subject: r.Name + "->" + peer,
			Evidence: append([]string{"cannot reach " + peer + ":"}, ev...)}
		switch {
		case contains(r.Diskful, peer):
			c.Status = StatusFail
			c.Message = fmt.Sprintf("%s cannot reach %s, which holds a copy of %s: the resource is one copy short",
				strings.Join(who, ", "), peer, r.Name)
			c.Fix = fmt.Sprintf("ssh %s sudo drbdadm adjust %s", sshTarget(in, peer), r.Name)
			if _, down := in.ProbeErrors[peer]; down {
				c.Message += "; the node itself does not answer SSH (see nodes.ssh)"
			}
		case contains(r.Tiebreakers, peer):
			c.Status = StatusWarn
			c.Message = fmt.Sprintf("the quorum tiebreaker %s of %s is unreachable from %s; the data is intact, but the next diskful failure loses quorum",
				peer, r.Name, strings.Join(who, ", "))
			c.Fix = fmt.Sprintf("start %s, or choose another tiebreaker: sds ha set-tiebreaker %s --node <node>", peer, r.Name)
		default:
			c.Status = StatusWarn
			c.Message = fmt.Sprintf("the diskless client %s of %s is unreachable from %s; it holds no copy, so the data is still fully redundant",
				peer, r.Name, strings.Join(who, ", "))
			if !contains(r.Clients, peer) {
				c.Message += " (it is not a client the controller has on record)"
			}
			c.Fix = fmt.Sprintf("start %s, or detach it: sds resource diskless detach %s %s", peer, r.Name, peer)
		}
		out = append(out, c)
	}
	return out
}

// faultDomains reports, once per failure domain, the resources whose every
// copy or quorum majority sits in it. On a cluster built on one hypervisor
// that is every resource, and one line per resource said nothing new.
func faultDomains(in *Input) []Check {
	by := map[string][]string{}
	for _, r := range sortedResources(in) {
		if r.FaultDomainRisk != "" {
			by[r.FaultDomainRisk] = append(by[r.FaultDomainRisk], r.Name)
		}
	}
	var out []Check
	for _, domain := range sortedKeys(by) {
		res := by[domain]
		out = append(out, Check{ID: "resource.fault_domain", Area: AreaResources, Subject: domain, Status: StatusWarn,
			Message: fmt.Sprintf("%s depend on one failure domain (%s) for every copy or the quorum majority",
				plural(len(res), "resource", "resources"), domain),
			Evidence: []string{strings.Join(res, ", ")},
			Fix:      "sds resource add-replica <resource> --node <node-in-another-domain>",
			Runbook:  "add-replica"})
	}
	return out
}
