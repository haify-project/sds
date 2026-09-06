package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// diagClient is the observability mock plus node collection, so a diagnose
// call exercises the whole path a real one takes.
type diagClient struct {
	obsClient

	collectReq   *sdspb.CollectNodeDiagnosticsRequest
	collectErr   error
	collectCalls int
	nodes        []*sdspb.NodeDiagnostics
}

func (c *diagClient) CollectNodeDiagnostics(_ context.Context, req *sdspb.CollectNodeDiagnosticsRequest) (*sdspb.CollectNodeDiagnosticsResponse, error) {
	c.collectCalls++
	c.collectReq = req
	if c.collectErr != nil {
		return nil, c.collectErr
	}
	return &sdspb.CollectNodeDiagnosticsResponse{
		Success:             true,
		Nodes:               c.nodes,
		AvailableCollectors: []string{"drbd_status", "storage"},
	}, nil
}

// A phantom peer on a node, reported through the tool the assistant actually
// calls. pkg/triage proves the matcher; this proves the wiring — the node
// collection reaches the analysis and the cause survives the trip out.
func TestDiagnoseReturnsAKnownCauseWithItsSteps(t *testing.T) {
	c := &diagClient{nodes: []*sdspb.NodeDiagnostics{{
		Node: "sds-b", Address: "192.168.123.227", Reachable: true,
		Collectors: []*sdspb.NodeCollectorOutput{{
			Collector: "drbd_status", Ok: true,
			Lines: []string{
				"sds-meta node-id:1 role:Primary suspended:no",
				"  sds-d node-id:3 connection:Connecting role:Unknown",
			},
		}},
	}}}

	session := connect(t, c, false)
	var out diagnoseOut
	callJSON(t, session, "sds_diagnose", map[string]any{}, &out)

	var f *diagnoseFinding
	for i := range out.Findings {
		if out.Findings[i].ID == "phantom-peer" {
			f = &out.Findings[i]
		}
	}
	if f == nil {
		t.Fatalf("phantom peer not reported; findings: %+v", out.Findings)
	}
	if !f.Known || f.Cause == "" || len(f.Advice) == 0 {
		t.Errorf("a known finding arrived without its cause or steps: %+v", f)
	}
	if f.Caution == "" {
		t.Error("the finding whose advice is irreversible carries no caution")
	}
	if len(f.Evidence) == 0 {
		t.Error("a finding arrived with no line to go and look at")
	}
	if out.Healthy {
		t.Error("a cluster with a critical finding was reported healthy")
	}
}

// A node fan-out that fails must not lose the controller's own records. The
// case this exists for is the one where nodes are unreachable *because*
// something is wrong, which is exactly when a diagnosis is wanted.
func TestDiagnoseSurvivesNodeCollectionFailing(t *testing.T) {
	c := &diagClient{collectErr: errors.New("dial tcp 192.168.123.227:22: i/o timeout")}
	session := connect(t, c, false)

	var out diagnoseOut
	callJSON(t, session, "sds_diagnose", map[string]any{}, &out)

	if out.Scanned.Events == 0 || out.Scanned.AuditEntries == 0 {
		t.Errorf("the controller's own records were lost when node collection failed: %+v", out.Scanned)
	}
	if !anyContains(out.Notes, "could not collect from nodes") {
		t.Errorf("nothing said the node evidence is missing: %v", out.Notes)
	}
}

// skip_nodes exists because a node that is down makes every collector wait out
// its SSH timeout. It must actually skip, and must say what the report lacks.
func TestDiagnoseSkipNodesDoesNotReachOut(t *testing.T) {
	c := &diagClient{}
	session := connect(t, c, false)

	var out diagnoseOut
	callJSON(t, session, "sds_diagnose", map[string]any{"skip_nodes": true}, &out)

	if c.collectCalls != 0 {
		t.Errorf("skip_nodes still collected from nodes (%d calls)", c.collectCalls)
	}
	if !anyContains(out.Notes, "node collection was skipped") {
		t.Errorf("a report with no node evidence did not say so: %v", out.Notes)
	}
}

// The window is the caller's control over how far back the journals are read.
// Dropping it silently would make "what happened this morning" return the last
// hour and look like an answer.
func TestDiagnoseWindowReachesTheCollector(t *testing.T) {
	c := &diagClient{}
	session := connect(t, c, false)

	var out diagnoseOut
	callJSON(t, session, "sds_diagnose", map[string]any{"window_minutes": 360}, &out)

	if c.collectReq.GetSinceMinutes() != 360 {
		t.Errorf("collector asked for %d minutes, caller said 360", c.collectReq.GetSinceMinutes())
	}
	if out.WindowMinutes != 360 {
		t.Errorf("report says window %d, caller said 360", out.WindowMinutes)
	}
}

// A caller that names collectors must have that reach the controller. Quietly
// running all of them would be slower than asked and, worse, would make a
// narrowed investigation return the same wall of evidence it was narrowing.
func TestDiagnoseForwardsTheChosenCollectors(t *testing.T) {
	c := &diagClient{}
	session := connect(t, c, false)

	var out diagnoseOut
	callJSON(t, session, "sds_diagnose", map[string]any{"collectors": []any{"drbd_status"}}, &out)
	if got := c.collectReq.GetCollectors(); len(got) != 1 || got[0] != "drbd_status" {
		t.Errorf("collectors = %v, want [drbd_status]", got)
	}
}

func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
