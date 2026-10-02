package inspect

import (
	"fmt"
	"strings"
)

// selfHAPromoter is the promoter config the Self-HA handoff installs.
const selfHAPromoter = "sds-ha-sds-meta.toml"

// checkSelfHA judges the controller's own HA: enough current copies of its
// database, a promoter on every node that may take over, and exactly one
// controller running, on the node that holds the database.
func checkSelfHA(in *Input) []Check {
	s := in.SelfHA
	if s == nil {
		return []Check{pass("selfha.disabled", AreaSelfHA, "Self-HA is not enabled; nothing to check")}
	}
	var out []Check
	r, ok := resourceNamed(in, s.Resource)
	if !ok {
		r = Resource{Name: s.Resource, Diskful: s.Nodes}
	}

	upToDate, known := 0, 0
	var ev []string
	for _, n := range r.Diskful {
		p, ok := in.probe(n)
		if !ok || !p.DRBDOK {
			continue
		}
		known++
		v := p.Resource(r.Name)
		if v == nil {
			ev = append(ev, in.nodeName(n)+": not up")
			continue
		}
		ev = append(ev, fmt.Sprintf("%s: %s, %s", in.nodeName(n), v.Role, strings.Join(v.diskStates(), "/")))
		if v.worstDisk(false) == "" {
			upToDate++
		}
	}
	if known > 0 && upToDate < 2 {
		out = append(out, Check{ID: "selfha.meta_replicas", Area: AreaSelfHA, Subject: r.Name, Status: StatusFail,
			Message: fmt.Sprintf("only %d of %d copies of the controller database are UpToDate; one more failure loses the controller",
				upToDate, len(r.Diskful)),
			Evidence: ev, Fix: "sds resource status " + r.Name})
	}

	out = append(out, promoterChecks(in, r, AreaSelfHA, "selfha", selfHAPromoter, "")...)

	var running, missingBin []string
	for _, n := range r.Diskful {
		p, ok := in.probe(n)
		if !ok {
			continue
		}
		if p.CtlActive == "active" {
			running = append(running, in.nodeName(n))
		}
		if p.CtlBin == "" {
			missingBin = append(missingBin, in.nodeName(n))
		}
	}
	prim, _ := primaries(in, r)
	switch {
	case len(in.unanswered(r.Diskful)) > 0 && len(running) < 2:
		// A node that did not answer may be the one running it.
	case len(running) == 0:
		out = append(out, Check{ID: "selfha.controller_active", Area: AreaSelfHA, Status: StatusFail,
			Message: "no Self-HA node reports sds-controller active, yet this inspection ran: the controller is running outside its promoter",
			Fix:     "sds ha self status"})
	case len(running) > 1:
		out = append(out, Check{ID: "selfha.controller_active", Area: AreaSelfHA, Subject: strings.Join(running, ","), Status: StatusFail,
			Message:  "sds-controller is active on more than one node; two controllers act on the cluster at once",
			Evidence: []string{"sds-meta Primary: " + strings.Join(prim, ", ")},
			Fix:      fmt.Sprintf("ssh %s sudo systemctl stop sds-controller", sshTarget(in, otherThan(running, prim)))})
	case len(prim) == 1 && running[0] != prim[0]:
		out = append(out, Check{ID: "selfha.controller_active", Area: AreaSelfHA, Subject: running[0], Status: StatusFail,
			Message: fmt.Sprintf("sds-controller runs on %s but sds-meta is Primary on %s; it is not using the replicated database", running[0], prim[0]),
			Fix:     fmt.Sprintf("ssh %s sudo systemctl stop sds-controller", sshTarget(in, running[0]))})
	}
	if len(missingBin) > 0 {
		out = append(out, Check{ID: "selfha.controller_binary", Area: AreaSelfHA, Subject: strings.Join(missingBin, ","), Status: StatusFail,
			Message: "no sds-controller binary is installed where the unit points on " + strings.Join(missingBin, ", ") + "; a failover there starts nothing",
			Fix:     "./scripts/deploy-all.sh " + strings.Join(r.Diskful, ",")})
	}
	if len(out) == 0 {
		out = append(out, pass("selfha.ready", AreaSelfHA, "%d of %d database copies UpToDate, promoter on every candidate, one controller active",
			upToDate, len(r.Diskful)))
	}
	return out
}

// otherThan returns the first of nodes not in keep, or the first node.
func otherThan(nodes, keep []string) string {
	for _, n := range nodes {
		if !contains(keep, n) {
			return n
		}
	}
	return nodes[0]
}
