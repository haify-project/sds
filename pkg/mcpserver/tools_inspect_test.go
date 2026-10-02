package mcpserver

import (
	"context"
	"testing"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

type inspectClient struct {
	ControllerClient
	gotID    string
	gotAreas []string
}

func (c *inspectClient) report() *sdspb.InspectionReport {
	return &sdspb.InspectionReport{
		Id: "12", Trigger: "manual", StartedAtUnixMs: 1790917200000, FinishedAtUnixMs: 1790917203000,
		Summary: &sdspb.InspectionSummary{Fail: 1, Pass: 3},
		Checks: []*sdspb.InspectionCheck{{
			Id: "gateway.no_primary", Area: "gateways", Subject: "blk", Status: "fail",
			Message: "blk should be serving but no node is Primary", Evidence: []string{"n1: Outdated"},
			Fix: "sds gateway start --resource blk",
		}},
	}
}

func (c *inspectClient) GetInspection(_ context.Context, id string) (*sdspb.InspectionReport, error) {
	c.gotID = id
	return c.report(), nil
}

func (c *inspectClient) RunInspection(_ context.Context, areas []string) (*sdspb.InspectionReport, error) {
	c.gotAreas = areas
	return c.report(), nil
}

// Both tools are reads, so a read-only server still offers them.
func TestInspectTools(t *testing.T) {
	mock := &inspectClient{}
	session := connect(t, mock, true)

	var out inspectReportOut
	callJSON(t, session, "sds_inspect_report", map[string]any{}, &out)
	if mock.gotID != "latest" || out.ID != "12" || out.Fail != 1 || len(out.Checks) != 1 {
		t.Fatalf("report: id %q, %+v", mock.gotID, out)
	}
	if c := out.Checks[0]; c.Fix != "sds gateway start --resource blk" || c.Evidence[0] != "n1: Outdated" {
		t.Errorf("check: %+v", c)
	}

	callJSON(t, session, "sds_inspect_run", map[string]any{"areas": []any{"gateways"}}, &out)
	if len(mock.gotAreas) != 1 || mock.gotAreas[0] != "gateways" || out.StartedAt != "2026-10-02T05:00:00Z" {
		t.Errorf("run: areas %v, %+v", mock.gotAreas, out)
	}
}
