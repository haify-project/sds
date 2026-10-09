package main

import (
	"bytes"
	"strings"
	"testing"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// Grouped by area in report order, failures first, each with its fix; a pass
// line carries no fix.
func TestPrintReportGroupsByAreaFailsFirst(t *testing.T) {
	r := &haifypb.InspectionReport{
		Id: "4", Trigger: "manual", StartedAtUnixMs: 1790917200000, FinishedAtUnixMs: 1790917201500,
		Summary: &haifypb.InspectionSummary{Fail: 1, Warn: 1, Pass: 1},
		Checks: []*haifypb.InspectionCheck{
			{Id: "nodes.root_fs", Area: "nodes", Subject: "n1", Status: "warn", Message: "root 88% full", Fix: "ssh n1 du"},
			{Id: "resource.replicas", Area: "resources", Status: "pass", Message: "all good", Fix: "never shown"},
			{Id: "nodes.ssh", Area: "nodes", Subject: "n2", Status: "fail", Message: "no answer",
				Evidence: []string{"i/o timeout"}, Fix: "haify node set-address n2 <addr>", Runbook: "renumber-nodes"},
		},
	}
	var b bytes.Buffer
	if err := printReport(&b, r, false); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	order := []string{"Inspection 4 (manual", "1 fail, 1 warn, 0 error, 1 pass", "RESOURCES", "all good",
		"NODES", "FAIL  nodes.ssh n2: no answer", "| i/o timeout", "fix: haify node set-address n2 <addr>",
		"runbook: renumber-nodes", "WARN  nodes.root_fs n1", "fix: ssh n1 du"}
	pos := 0
	for _, want := range order {
		i := strings.Index(out[pos:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", want, out)
		}
		pos += i + len(want)
	}
	if strings.Contains(out, "never shown") {
		t.Errorf("a pass line printed a fix:\n%s", out)
	}

	b.Reset()
	if err := printReport(&b, r, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"started_at_unix_ms"`) {
		t.Errorf("json uses the proto field names:\n%s", b.String())
	}
}
