package inspect

import (
	"strings"
	"testing"
)

func TestRunFiltersAreasAndSorts(t *testing.T) {
	in := cluster()
	in.Probes["n1"].RootUse = 99
	in.Probes["n2"].RootUse = 88
	checks := Run(in, []Area{AreaNodes, AreaHygiene})
	for _, c := range checks {
		if c.Area != AreaNodes && c.Area != AreaHygiene {
			t.Fatalf("area %s was not asked for", c.Area)
		}
	}
	if checks[0].Status != StatusFail || checks[1].Status != StatusWarn || checks[len(checks)-1].Area != AreaHygiene {
		t.Errorf("want nodes fail, warn, then hygiene:\n%s", dump(checks))
	}
}

func TestEveryAreaAnswersOnAnEmptyCluster(t *testing.T) {
	checks := Run(&Input{}, nil)
	seen := map[Area]bool{}
	for _, c := range checks {
		seen[c.Area] = true
	}
	for _, a := range Areas {
		if !seen[a] {
			t.Errorf("area %s said nothing", a)
		}
	}
}

func TestSummaryWorstAndHeadline(t *testing.T) {
	checks := []Check{
		{ID: "a", Status: StatusPass},
		{ID: "nodes.root_fs", Subject: "n1", Status: StatusWarn},
		{ID: "gateway.no_primary", Subject: "blk", Status: StatusFail},
		{ID: "nodes.ssh", Subject: "n2", Status: StatusError},
	}
	s := Summarize(checks)
	if s != (Summary{Pass: 1, Warn: 1, Fail: 1, Error: 1}) {
		t.Errorf("summary %+v", s)
	}
	if Worst(checks) != StatusFail || Worst(checks[:2]) != StatusWarn || Worst(nil) != StatusPass {
		t.Error("worst")
	}
	r := &Report{ID: "9", Summary: s, Checks: checks}
	h := Headline(r, 2)
	if !strings.HasPrefix(h, "Inspection 9: 1 fail, 1 warn, 1 error, 1 pass. gateway.no_primary blk; nodes.ssh n2; and 1 more") {
		t.Errorf("headline: %s", h)
	}
	if !strings.HasSuffix(h, "sds inspect show 9") {
		t.Errorf("headline should say where the details are: %s", h)
	}
	if !StatusError.AtLeast(StatusWarn) || StatusWarn.AtLeast(StatusFail) || ParseStatus("warning") != StatusWarn {
		t.Error("status ordering")
	}
}
