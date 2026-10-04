package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// Observability tools: what the cluster DID, not just what it is.
//
// Every other tool here answers "what does the cluster look like right now".
// An assistant with only those can describe a cluster but cannot explain how it
// reached that state — which is most of what anyone actually asks after
// something goes wrong. These three cover the three separate records the
// controller keeps: operational events (a replica degraded, a Primary moved),
// the audit trail (who called what), and the controller's own log.

type eventOut struct {
	ID        uint64            `json:"id" jsonschema:"monotonic id; a gap means the reader fell behind and lost events"`
	Type      string            `json:"type" jsonschema:"resource.degraded, resource.failover, resource.no_primary, resource.promoted, node.unreachable, wan.degraded, resource.out_of_sync, pool.data_near_full, pool.data_full, pool.metadata_near_full, pool.metadata_full, pool.out_of_space, pool.snapshots_removed, pool.snapshots_locked, audit.shipping_failed, audit.truncated, backup.failed, inspection.completed"`
	Severity  string            `json:"severity" jsonschema:"info, warning, or critical"`
	Status    string            `json:"status" jsonschema:"firing when a condition starts, resolved when it clears, info for one-shot events"`
	Resource  string            `json:"resource,omitempty"`
	Node      string            `json:"node,omitempty"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty" jsonschema:"type-specific fields: disk_state/repl_state for degrade, from/to for failover"`
	Timestamp string            `json:"timestamp" jsonschema:"RFC3339"`
}

type eventListIn struct {
	Limit       int32    `json:"limit,omitempty" jsonschema:"maximum events to return; 0 means a server-chosen default"`
	MinSeverity string   `json:"min_severity,omitempty" jsonschema:"info (default), warning, or critical"`
	Types       []string `json:"types,omitempty" jsonschema:"exact event types to keep; empty for all"`
	Resource    string   `json:"resource,omitempty" jsonschema:"only events for this resource"`
	SinceID     uint64   `json:"since_id,omitempty" jsonschema:"only events newer than this id, to resume without re-reading"`
}

type eventListOut struct {
	Events []eventOut `json:"events" jsonschema:"oldest first"`
	// Published is the total ever published; compared with the oldest id
	// returned it reveals whether history has already been discarded.
	Published uint64 `json:"published"`
	Dropped   uint64 `json:"dropped" jsonschema:"events lost because a subscriber could not keep up"`
}

type auditOut struct {
	Timestamp string `json:"timestamp"`
	Method    string `json:"method"`
	User      string `json:"user,omitempty"`
	Client    string `json:"client,omitempty"`
	Target    string `json:"target,omitempty"`
	Result    string `json:"result"`
	Granted   bool   `json:"granted"`
	LatencyMs int64  `json:"latency_ms"`
	Node      string `json:"node,omitempty" jsonschema:"which node served the call; the controller relocates"`
	Error     string `json:"error,omitempty"`
}

type auditListIn struct {
	Limit        int32  `json:"limit,omitempty"`
	Method       string `json:"method,omitempty" jsonschema:"exact RPC name filter, e.g. EvictHa"`
	Target       string `json:"target,omitempty"`
	User         string `json:"user,omitempty"`
	FailuresOnly bool   `json:"failures_only,omitempty" jsonschema:"only entries whose result is not OK"`
	SinceUnixMs  int64  `json:"since_unix_ms,omitempty"`
}

type auditListOut struct {
	Events []auditOut `json:"events" jsonschema:"newest first"`
	Total  int64      `json:"total" jsonschema:"entries held in total, so a caller knows if this is everything"`
}

type logOut struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Logger    string `json:"logger,omitempty"`
	Caller    string `json:"caller,omitempty"`
	Message   string `json:"message"`
}

type logListIn struct {
	Limit    int32  `json:"limit,omitempty"`
	MinLevel string `json:"min_level,omitempty" jsonschema:"debug, info, warn, or error"`
	Contains string `json:"contains,omitempty" jsonschema:"substring the message must contain"`
}

type logListOut struct {
	Entries []logOut `json:"entries"`
	Node    string   `json:"node,omitempty" jsonschema:"the node whose buffer this is — the active controller"`
	// Truncated means the ring wrapped, i.e. older lines are already gone.
	Truncated bool `json:"truncated"`
}

func rfc3339(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// registerObservabilityTools adds event, audit, and controller-log tools.
func (s *Server) registerObservabilityTools(srv *mcp.Server) {
	addRead(s, srv, readOnlyTool("sds_event_list", "List cluster events",
		"List operational notifications the controller has raised: replicas going degraded, a resource's Primary "+
			"moving, a node becoming unreachable, WAN replication breaking, a thin pool filling up. Each event has "+
			"a severity and a status (firing when a condition starts, resolved when it clears), so a firing event "+
			"with no matching resolved is still outstanding. This is the tool to reach for when asked what went "+
			"wrong or what changed — the other tools only show the cluster's current shape. "+
			"pool.* events carry the pool name in resource and the node in node; pair them with sds_pool_list for "+
			"the current percentages, since an outstanding event says a threshold was crossed, not where the pool "+
			"is now. Requires [alert] enabled in controller.toml."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in eventListIn) (*mcp.CallToolResult, eventListOut, error) {
			resp, err := s.client.ListEvents(ctx, &sdspb.ListEventsRequest{
				Limit:       in.Limit,
				MinSeverity: in.MinSeverity,
				Types:       in.Types,
				Resource:    in.Resource,
				SinceId:     in.SinceID,
			})
			if err != nil {
				return nil, eventListOut{}, err
			}
			out := eventListOut{
				Events:    make([]eventOut, 0, len(resp.Events)),
				Published: resp.Published,
				Dropped:   resp.Dropped,
			}
			for _, e := range resp.Events {
				out.Events = append(out.Events, eventOut{
					ID:        e.Id,
					Type:      e.Type,
					Severity:  e.Severity,
					Status:    e.Status,
					Resource:  e.Resource,
					Node:      e.Node,
					Message:   e.Message,
					Details:   e.Details,
					Timestamp: rfc3339(e.TimestampUnixMs),
				})
			}
			return nil, out, nil
		})

	addRead(s, srv, readOnlyTool("sds_audit_list", "List audit trail",
		"List who called which management RPC, when, from where, and whether it was allowed. Persisted on the "+
			"controller's replicated volume, so the history follows the controller across a failover. Use "+
			"failures_only to find rejected or errored calls."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in auditListIn) (*mcp.CallToolResult, auditListOut, error) {
			resp, err := s.client.ListAuditEvents(ctx, &sdspb.ListAuditEventsRequest{
				Limit:        in.Limit,
				Method:       in.Method,
				Target:       in.Target,
				User:         in.User,
				FailuresOnly: in.FailuresOnly,
				SinceUnixMs:  in.SinceUnixMs,
			})
			if err != nil {
				return nil, auditListOut{}, err
			}
			out := auditListOut{Events: make([]auditOut, 0, len(resp.Events)), Total: resp.Total}
			for _, e := range resp.Events {
				out.Events = append(out.Events, auditOut{
					Timestamp: rfc3339(e.TimestampUnixMs),
					Method:    e.Method,
					User:      e.User,
					Client:    e.Client,
					Target:    e.Target,
					Result:    e.Result,
					Granted:   e.Granted,
					LatencyMs: e.LatencyMs,
					Node:      e.Node,
					Error:     e.Error,
				})
			}
			return nil, out, nil
		})

	addRead(s, srv, readOnlyTool("sds_log_list", "Read controller logs",
		"Read recent lines from the active controller's own log ring. Deliberately not persisted: it describes what "+
			"a running controller is doing right now, and once the controller has relocated its old node's output "+
			"belongs to a different process. For durable history use sds_audit_list."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in logListIn) (*mcp.CallToolResult, logListOut, error) {
			resp, err := s.client.ListControllerLogs(ctx, &sdspb.ListControllerLogsRequest{
				Limit:    in.Limit,
				Level:    in.MinLevel,
				Contains: in.Contains,
			})
			if err != nil {
				return nil, logListOut{}, err
			}
			out := logListOut{
				Entries:   make([]logOut, 0, len(resp.Entries)),
				Node:      resp.Node,
				Truncated: resp.Truncated,
			}
			for _, e := range resp.Entries {
				out.Entries = append(out.Entries, logOut{
					Timestamp: rfc3339(e.TimestampUnixMs),
					Level:     e.Level,
					Logger:    e.Logger,
					Caller:    e.Caller,
					Message:   e.Message,
				})
			}
			return nil, out, nil
		})
}

type notifyChannelOut struct {
	Name        string `json:"name"`
	Kind        string `json:"kind" jsonschema:"message format: generic, feishu, slack, wecom or dingtalk"`
	URL         string `json:"url"`
	MinSeverity string `json:"min_severity"`
	Enabled     bool   `json:"enabled" jsonschema:"false means the channel is configured but muted and receives nothing"`
	HasSecret   bool   `json:"has_secret"`
}

type notifyChannelListOut struct {
	Channels []notifyChannelOut `json:"channels"`
	Kinds    []string           `json:"kinds" jsonschema:"message formats this controller can render"`
}

// registerNotifyTools exposes where alerts go, and the one operation that can
// tell whether they arrive.
//
// There is deliberately no tool for CREATING a channel. A bot URL is a bearer
// credential — anyone holding it can post into the channel — and anything
// passed as a tool argument is recorded in the conversation that passed it.
// The same rule keeps sds_backup_target_add out of the tool list.
func (s *Server) registerNotifyTools(srv *mcp.Server) {
	addRead(s, srv, readOnlyTool("sds_notify_channel_list", "List alert notification channels",
		"List where this cluster's alerts are delivered — Feishu, Slack, WeCom, DingTalk or a plain webhook. "+
			"Use this when asked whether anyone would be told about a problem: a cluster with alerting enabled "+
			"and no enabled channel raises events that nobody receives. Bot URLs are returned; signing secrets "+
			"never are."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, notifyChannelListOut, error) {
			channels, kinds, err := s.client.ListNotifyChannels(ctx)
			if err != nil {
				return nil, notifyChannelListOut{}, err
			}
			out := notifyChannelListOut{Channels: make([]notifyChannelOut, 0, len(channels)), Kinds: kinds}
			for _, c := range channels {
				out.Channels = append(out.Channels, notifyChannelOut{
					Name: c.Name, Kind: c.Kind, URL: c.Url,
					MinSeverity: c.MinSeverity, Enabled: c.Enabled, HasSecret: c.HasSecret,
				})
			}
			return nil, out, nil
		})

	addWrite(s, srv, writeTool("sds_notify_channel_test", "Send a test alert to one channel",
		"Deliver one synthetic message to a single channel and report what the service actually said. This is the "+
			"only way to know a channel works: Feishu, WeCom and DingTalk answer HTTP 200 for a message they "+
			"refused and put the reason in the body, so a wrong bot URL or a missing signature looks like success "+
			"from the outside. The message does not go through the event bus, so it reaches no other channel and "+
			"does not appear in the event history."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Name string `json:"name" jsonschema:"channel to test"`
		}) (*mcp.CallToolResult, opResult, error) {
			msg, err := s.client.TestNotifyChannel(ctx, in.Name)
			if err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(msg), nil
		})
}
