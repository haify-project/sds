package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

// obsClient implements only the observability slice of ControllerClient.
//
// mockClient embeds a nil ControllerClient, so a tool that is registered but
// never called still passes the registration test while panicking the moment
// anyone uses it. These tests call each new tool for real.
type obsClient struct {
	ControllerClient

	eventReq *sdspb.ListEventsRequest
	auditReq *sdspb.ListAuditEventsRequest
	logReq   *sdspb.ListControllerLogsRequest
}

func (c *obsClient) ListEvents(_ context.Context, req *sdspb.ListEventsRequest) (*sdspb.ListEventsResponse, error) {
	c.eventReq = req
	return &sdspb.ListEventsResponse{
		Success:   true,
		Published: 7,
		Dropped:   1,
		Events: []*sdspb.Event{{
			Id: 7, Type: "resource.failover", Severity: "warning", Status: "firing",
			Resource: "vmstore", Node: "orange2",
			Message:         "resource vmstore failed over: Primary moved from orange1 to orange2",
			Details:         map[string]string{"from": "orange1", "to": "orange2"},
			TimestampUnixMs: 1786105730000,
		}},
	}, nil
}

func (c *obsClient) ListAuditEvents(_ context.Context, req *sdspb.ListAuditEventsRequest) (*sdspb.ListAuditEventsResponse, error) {
	c.auditReq = req
	return &sdspb.ListAuditEventsResponse{
		Success: true, Total: 42,
		Events: []*sdspb.AuditEvent{{
			TimestampUnixMs: 1786105730000, Method: "EvictHa", User: "ops", Target: "vmstore",
			Result: "PermissionDenied", Granted: false, LatencyMs: 3, Node: "orange2",
			Error: "administrator role required",
		}},
	}, nil
}

func (c *obsClient) ListControllerLogs(_ context.Context, req *sdspb.ListControllerLogsRequest) (*sdspb.ListControllerLogsResponse, error) {
	c.logReq = req
	return &sdspb.ListControllerLogsResponse{
		Success: true, Node: "orange2", Truncated: true,
		Entries: []*sdspb.ControllerLogEntry{{
			TimestampUnixMs: 1786105730000, Level: "warn", Logger: "alert",
			Caller: "alert/alert.go:1", Message: "list nodes failed",
		}},
	}, nil
}

func callJSON[T any](t *testing.T, session *mcp.ClientSession, name string, args map[string]any, out *T) {
	t.Helper()
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned tool error: %v", name, res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal %s result: %v", name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal %s result: %v", name, err)
	}
}

func TestEventListTool(t *testing.T) {
	mock := &obsClient{}
	session := connect(t, mock, false)

	var out eventListOut
	callJSON(t, session, "sds_event_list", map[string]any{
		"min_severity": "warning",
		"resource":     "vmstore",
		"since_id":     3,
	}, &out)

	if mock.eventReq == nil {
		t.Fatal("ListEvents was never called")
	}
	if mock.eventReq.MinSeverity != "warning" || mock.eventReq.Resource != "vmstore" || mock.eventReq.SinceId != 3 {
		t.Errorf("filters not forwarded: %+v", mock.eventReq)
	}

	if len(out.Events) != 1 {
		t.Fatalf("want 1 event, got %d", len(out.Events))
	}
	e := out.Events[0]
	if e.Type != "resource.failover" || e.Status != "firing" {
		t.Errorf("event fields lost: %+v", e)
	}
	// Details carry the whole point of a failover event: where it went.
	if e.Details["from"] != "orange1" || e.Details["to"] != "orange2" {
		t.Errorf("details lost: %+v", e.Details)
	}
	if e.Timestamp != "2026-08-07T12:28:50Z" {
		t.Errorf("timestamp not rendered as RFC3339: %q", e.Timestamp)
	}
	// Dropped is how a caller learns the history is incomplete; losing it
	// would make a truncated answer look authoritative.
	if out.Published != 7 || out.Dropped != 1 {
		t.Errorf("counters lost: published=%d dropped=%d", out.Published, out.Dropped)
	}
}

func TestAuditListTool(t *testing.T) {
	mock := &obsClient{}
	session := connect(t, mock, false)

	var out auditListOut
	callJSON(t, session, "sds_audit_list", map[string]any{"failures_only": true}, &out)

	if mock.auditReq == nil || !mock.auditReq.FailuresOnly {
		t.Fatalf("failures_only not forwarded: %+v", mock.auditReq)
	}
	if len(out.Events) != 1 || out.Total != 42 {
		t.Fatalf("unexpected result: %+v", out)
	}
	a := out.Events[0]
	if a.Granted || a.Result != "PermissionDenied" || a.Error == "" {
		t.Errorf("a denied call must stay legible as denied: %+v", a)
	}
	if a.Node != "orange2" {
		t.Errorf("serving node lost; a trail spanning a failover is ambiguous without it")
	}
}

func TestLogListTool(t *testing.T) {
	mock := &obsClient{}
	session := connect(t, mock, false)

	var out logListOut
	callJSON(t, session, "sds_log_list", map[string]any{"min_level": "warn", "contains": "nodes"}, &out)

	if mock.logReq == nil {
		t.Fatal("ListControllerLogs was never called")
	}
	// The proto field is `level`, not `min_level` — an easy mismatch to make,
	// and it would silently return everything instead of filtering.
	if mock.logReq.Level != "warn" || mock.logReq.Contains != "nodes" {
		t.Errorf("filters not forwarded: %+v", mock.logReq)
	}
	if len(out.Entries) != 1 || out.Entries[0].Level != "warn" {
		t.Fatalf("unexpected entries: %+v", out.Entries)
	}
	if !out.Truncated || out.Node != "orange2" {
		t.Errorf("truncation/node lost: %+v", out)
	}
}

// Observability is read-only, so it must survive --read-only. A responder that
// loses its ability to explain an incident in the safe mode is the wrong way
// round.
func TestObservabilityToolsSurviveReadOnly(t *testing.T) {
	session := connect(t, &obsClient{}, true)
	tools := listTools(t, session)

	for _, want := range []string{"sds_event_list", "sds_audit_list", "sds_log_list"} {
		if _, found := tools[want]; !found {
			t.Errorf("%s must remain available in read-only mode", want)
		}
	}
	for _, unwanted := range []string{
		"sds_resource_remove_replica", "sds_node_drain", "sds_zfs_pool_delete",
		"sds_pool_convert_thin", "sds_ha_sync_toml", "sds_wan_repair",
	} {
		if _, found := tools[unwanted]; found {
			t.Errorf("%s mutates and must be absent in read-only mode", unwanted)
		}
	}
}
