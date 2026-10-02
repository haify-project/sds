package inspect

import (
	"fmt"
	"sort"
	"strings"
)

// checkGateways requires every gateway that is not stopped to have exactly
// one Primary, and its promoter config to sit on exactly the nodes that can
// take it over.
func checkGateways(in *Input) []Check {
	var out []Check
	gws := append([]Gateway(nil), in.Gateways...)
	sort.Slice(gws, func(i, j int) bool { return gws[i].Resource < gws[j].Resource })
	serving := 0
	for _, g := range gws {
		if !g.Serving() {
			continue
		}
		serving++
		r, ok := resourceNamed(in, g.Resource)
		if !ok {
			out = append(out, Check{ID: "gateway.orphan", Area: AreaGateways, Subject: g.Resource, Status: StatusFail,
				Message: fmt.Sprintf("%s gateway is recorded as %s but its resource %s does not exist", g.Type, g.Status, g.Resource),
				Fix:     fmt.Sprintf("sds gateway delete --resource %s", g.Resource)})
			continue
		}
		why := fmt.Sprintf("%s gateway, %s", g.Type, orDash(g.Status))
		out = append(out, primaryCheck(in, r, AreaGateways, "gateway", why,
			fmt.Sprintf("sds gateway start --resource %s", g.Resource))...)
		// Start re-enables a .disabled config and retires one left on a node
		// without a replica.
		out = append(out, promoterChecks(in, r, AreaGateways, "gateway", fmt.Sprintf("sds-%s-%s.toml", g.Type, g.Resource),
			fmt.Sprintf("sds gateway start --resource %s", g.Resource))...)
	}
	if len(out) == 0 {
		out = append(out, pass("gateway.serving", AreaGateways,
			"%s serving: each has one Primary and its promoter on every diskful node", plural(serving, "gateway", "gateways")))
	}
	return out
}

// promoterChecks requires a promoter config on every diskful node of r and on
// none of its tiebreakers or diskless clients.
//
// Missing on a diskful node means failover cannot land there. Present on a
// tiebreaker is worse: drbd-reactor there tries to start the service stack on
// a node with no data, and a stack that half-starts can hold a mount or a
// lock that the real takeover then waits on.
//
// fix repairs both; empty means copy the config from a node that has it.
func promoterChecks(in *Input, r Resource, area Area, prefix, file, fix string) []Check {
	var out []Check
	var missing, disabled []string
	source := ""
	for _, n := range r.Diskful {
		p, ok := in.probe(n)
		if !ok {
			continue
		}
		switch {
		case p.HasReactorConf(file):
			if source == "" {
				source = in.nodeName(n)
			}
		case p.HasReactorConf(file + ".disabled"):
			disabled = append(disabled, in.nodeName(n))
		default:
			missing = append(missing, in.nodeName(n))
		}
	}
	if len(missing)+len(disabled) > 0 {
		var ev []string
		if len(missing) > 0 {
			ev = append(ev, "no /etc/drbd-reactor.d/"+file+" on: "+strings.Join(missing, ", "))
		}
		if len(disabled) > 0 {
			ev = append(ev, "only the .disabled copy on: "+strings.Join(disabled, ", "))
		}
		out = append(out, Check{ID: prefix + ".promoter_missing", Area: area, Subject: r.Name, Status: StatusWarn,
			Message:  "the promoter config is not active on every diskful node, so a failover cannot land on " + strings.Join(append(missing, disabled...), ", "),
			Evidence: ev,
			Fix:      promoterFix(in, fix, file, source, append(missing, disabled...))})
	}
	for _, n := range append(append([]string{}, r.Tiebreakers...), r.Clients...) {
		p, ok := in.probe(n)
		if !ok || !p.HasReactorConf(file) {
			continue
		}
		name := in.nodeName(n)
		out = append(out, Check{ID: prefix + ".promoter_on_diskless", Area: area, Subject: r.Name + "@" + name, Status: StatusFail,
			Message: fmt.Sprintf("%s holds no data for %s but has its promoter config; drbd-reactor there can start the service stack without the data",
				name, r.Name),
			Evidence: []string{"/etc/drbd-reactor.d/" + file + " present on " + name},
			Fix: firstNonEmpty(fix, fmt.Sprintf("ssh %s sudo rm /etc/drbd-reactor.d/%s && ssh %s sudo systemctl reload drbd-reactor",
				sshTarget(in, name), file, sshTarget(in, name)))})
	}
	return out
}

func resourceNamed(in *Input, name string) (Resource, bool) {
	for _, r := range in.Resources {
		if r.Name == name {
			return r, true
		}
	}
	return Resource{}, false
}

// promoterFix copies the promoter config from source to the first node that
// lacks it, when no command was given that repairs it.
func promoterFix(in *Input, fix, file, source string, lacking []string) string {
	if fix != "" || source == "" || len(lacking) == 0 {
		return fix
	}
	path := "/etc/drbd-reactor.d/" + file
	dst := sshTarget(in, lacking[0])
	return fmt.Sprintf("ssh %s sudo cat %s | ssh %s sudo tee %s >/dev/null && ssh %s sudo systemctl reload drbd-reactor",
		sshTarget(in, source), path, dst, path, dst)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
