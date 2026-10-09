package inspect

import (
	"fmt"
	"sort"
	"strings"
)

// checker is one family of checks over the whole input.
type checker struct {
	area Area
	run  func(*Input) []Check
}

var checkers = []checker{
	{AreaResources, checkResources},
	{AreaGateways, checkGateways},
	{AreaNodes, checkNodes},
	{AreaPools, checkPools}, {AreaPools, checkDisks},
	{AreaBackups, checkBackups},
	{AreaAlerts, checkAlerts},
	{AreaSelfHA, checkSelfHA},
	{AreaTLS, checkTLS},
	{AreaHygiene, checkHygiene},
}

// Run evaluates every check in the requested areas (all when empty) and
// returns them sorted.
func Run(in *Input, areas []Area) []Check {
	want := map[Area]bool{}
	for _, a := range areas {
		want[a] = true
	}
	var out []Check
	for _, c := range checkers {
		if len(want) > 0 && !want[c.area] {
			continue
		}
		out = append(out, c.run(in)...)
	}
	Sort(out)
	return out
}

// PoolSamples extracts the thin pool fill levels to store with a report.
func PoolSamples(in *Input) []PoolSample {
	var out []PoolSample
	for _, name := range sortedKeys(in.Probes) {
		p := in.Probes[name]
		if p == nil {
			continue
		}
		for _, lv := range p.LVs {
			if lv.Segtype == "thin-pool" && lv.HasUsage && managedVG(lv.VG) {
				out = append(out, PoolSample{Node: name, Pool: lv.VG + "/" + lv.Name,
					DataPercent: lv.DataPercent, MetaPercent: lv.MetaPercent})
			}
		}
	}
	return out
}

// Headline is a one-paragraph summary of a report for a notification: the
// counts and the most serious items, each with its subject.
func Headline(r *Report, max int) string {
	s := r.Summary
	head := fmt.Sprintf("Inspection %s: %d fail, %d warn, %d error, %d pass", r.ID, s.Fail, s.Warn, s.Error, s.Pass)
	var items []string
	for _, st := range []Status{StatusFail, StatusError, StatusWarn} {
		for _, c := range r.Checks {
			if c.Status != st || len(items) >= max {
				continue
			}
			item := c.ID
			if c.Subject != "" {
				item += " " + c.Subject
			}
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return head + "."
	}
	more := s.Fail + s.Warn + s.Error - len(items)
	line := head + ". " + strings.Join(items, "; ")
	if more > 0 {
		line += fmt.Sprintf("; and %d more", more)
	}
	return line + ". Details: haify inspect show " + r.ID
}

// managedVG reports whether a volume group is a Haify pool.
func managedVG(vg string) bool { return strings.HasPrefix(vg, "haify_") }

func pass(id string, area Area, msg string, args ...any) Check {
	return Check{ID: id, Area: area, Status: StatusPass, Message: fmt.Sprintf(msg, args...)}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
