package triage

import (
	"strings"
	"time"
)

// Known failure modes: the ones SDS knows the cause of.
//
// Everything else in this package groups evidence and stops. These nine state
// a cause and give steps, and the bar for joining them is high: the match has
// to be specific enough that a false positive is hard to construct, and the
// advice has to be right without adaptation. A step an operator has to adjust
// is a step they will adjust wrongly while the cluster is down.
//
// Each matcher returns findings it can show the line for. None of them infers:
// if the evidence is not there, the mode does not fire and the generic
// grouping reports the lines as what they are.

type matcher func(Input) []Finding

var matchers = []matcher{
	matchPhantomPeer,
	matchSplitBrain,
	matchThinPool,
	matchFilesystemAborted,
	matchDRBDModuleMissing,
	matchPromoterStartFailed,
	matchUnplannedFailover,
	matchNoPrimary,
	matchNodeUnreachable,
	matchMountFull,
}

func knownFindings(in Input) []Finding {
	var out []Finding
	for _, m := range matchers {
		out = append(out, m(in)...)
	}
	return out
}

// ── helpers ─────────────────────────────────────────────────────────────

// nodeLinesMatching collects every line from the named collectors that a
// predicate accepts, with the node it came from.
func nodeLinesMatching(in Input, keep func(string) bool, collectors ...string) ([]Evidence, []string) {
	var ev []Evidence
	nodes := map[string]bool{}
	for _, n := range in.Nodes {
		for _, name := range collectors {
			c, ok := n.Collector(name)
			if !ok {
				continue
			}
			for _, line := range c.Lines {
				if !keep(line) {
					continue
				}
				nodes[n.Node] = true
				ev = append(ev, Evidence{Source: n.Node + "/" + name, Node: n.Node, Line: strings.TrimSpace(line)})
			}
		}
	}
	return ev, sortedKeys(nodes)
}

// record walks one collector's lines on every node.
func record(in Input, collector string, fn func(node, line string)) {
	for _, n := range in.Nodes {
		c, ok := n.Collector(collector)
		if !ok || !c.Ok {
			continue
		}
		for _, line := range c.Lines {
			fn(n.Node, line)
		}
	}
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
