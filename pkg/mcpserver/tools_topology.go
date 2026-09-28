package mcpserver

import (
	"context"
	"fmt"
	"strings"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Topology and node-lifecycle tools.
//
// These change the shape of a cluster rather than its contents: how many copies
// a resource has and where, which nodes are eligible to hold them, and how a
// node is taken out of service. They were all reachable over gRPC and the CLI
// but had no MCP tool, so an assistant could describe a topology problem and
// not act on it.

type replicaIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name"`
	Node     string `json:"node" jsonschema:"node to add or remove the replica on"`
}

type tiebreakerIn struct {
	Resource string `json:"resource"`
	Node     string `json:"node" jsonschema:"node to hold the diskless quorum tiebreaker"`
}

type addDRIn struct {
	Resource      string `json:"resource"`
	DRNode        string `json:"dr_node" jsonschema:"registered node at the off-site location"`
	DREndpoint    string `json:"dr_endpoint" jsonschema:"public address the primary site dials to reach the DR"`
	WANPort       uint32 `json:"wan_port,omitempty" jsonschema:"base mTLS WAN port; 0 auto-allocates"`
	EgressAddress string `json:"egress_address,omitempty" jsonschema:"source address the primary site egresses from, when it matters"`
}

type addDROut struct {
	Status  string `json:"status"`
	WANPort uint32 `json:"wan_port" jsonschema:"the base WAN port actually used"`
}

type wanRepairIn struct {
	Resource string `json:"resource"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"report the plan without touching anything; do this first, repair restarts tunnels"`
}

type wanRepairOut struct {
	Status            string   `json:"status"`
	Message           string   `json:"message"`
	ExpectedLegs      []string `json:"expected_legs,omitempty"`
	RemovedLegs       []string `json:"removed_legs,omitempty" jsonschema:"stale instances removed, or under dry_run would be removed"`
	AlreadyConsistent bool     `json:"already_consistent"`
}

type nodeNameIn struct {
	Node string `json:"node" jsonschema:"node name"`
}

type drainOut struct {
	Status    string   `json:"status"`
	Evacuated []string `json:"evacuated,omitempty" jsonschema:"resources moved off the node"`
}

type nodeLabelsIn struct {
	Node    string            `json:"node"`
	Labels  map[string]string `json:"labels" jsonschema:"key/value labels used for placement decisions"`
	Replace bool              `json:"replace,omitempty" jsonschema:"replace all labels instead of merging"`
}

type convertThinIn struct {
	Node string `json:"node"`
	Pool string `json:"pool" jsonschema:"LVM volume group to rebuild as a thin pool"`
}

type haTomlIn struct {
	Resource string `json:"resource"`
}

type haTomlOut struct {
	Resource string `json:"resource"`
	Content  string `json:"content" jsonschema:"the drbd-reactor promoter TOML currently on disk"`
	Path     string `json:"path,omitempty"`
}

type haTomlSyncIn struct {
	Resource string `json:"resource"`
	Content  string `json:"content" jsonschema:"full promoter TOML to write to every node"`
}

type agentOut struct {
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	ShortDesc string `json:"shortdesc,omitempty"`
}

type agentListOut struct {
	Agents []agentOut `json:"agents"`
}

type agentMetaIn struct {
	Provider string `json:"provider" jsonschema:"OCF provider, e.g. heartbeat"`
	Name     string `json:"name" jsonschema:"agent name, e.g. IPaddr2"`
}

type agentParamOut struct {
	Name      string `json:"name"`
	Required  bool   `json:"required"`
	Type      string `json:"type,omitempty"`
	Default   string `json:"default,omitempty"`
	ShortDesc string `json:"shortdesc,omitempty"`
}

type agentMetaOut struct {
	Provider   string          `json:"provider,omitempty"`
	Name       string          `json:"name"`
	Version    string          `json:"version,omitempty"`
	ShortDesc  string          `json:"shortdesc,omitempty"`
	LongDesc   string          `json:"longdesc,omitempty"`
	Parameters []agentParamOut `json:"parameters" jsonschema:"every parameter the agent accepts; required ones must be supplied"`
}

type haStatusIn struct {
	Resource string `json:"resource,omitempty" jsonschema:"empty for every HA resource"`
}

type haPromoterOut struct {
	Resource  string `json:"resource"`
	PrimaryOn string `json:"primary_on,omitempty"`
	Status    string `json:"status"`
}

type haStatusOut struct {
	Promoters []haPromoterOut `json:"promoters"`
}

// registerTopologyTools adds replica, DR, drain, label and HA-config tools.
func (s *Server) registerTopologyTools(srv *mcp.Server) {
	addWrite(s, srv, writeTool("sds_resource_add_replica", "Add a replica",
		"Add a full (diskful) copy of a running resource on another node. The new copy syncs in the background; "+
			"the resource stays usable throughout."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in replicaIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.AddReplica(ctx, in.Resource, in.Node); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok("replica of " + in.Resource + " added on " + in.Node), nil
		})

	addWrite(s, srv, destructiveTool("sds_resource_remove_replica", "Remove a replica",
		"Take a full copy of a resource out permanently. Refused when the node is Primary, when it is the quorum "+
			"tiebreaker or the off-site DR, or when fewer than two diskful copies would remain. Unlike a conversion, "+
			"whose reduced-redundancy window closes when the resync finishes, this does not close — confirm before use."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in replicaIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.RemoveReplica(ctx, in.Resource, in.Node); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok("replica of " + in.Resource + " removed from " + in.Node), nil
		})

	addWrite(s, srv, writeTool("sds_resource_attach_diskless", "Attach a diskless client",
		"Let a node use a resource over the DRBD network without storing a local copy. This is how a workload runs "+
			"on a node that holds no replica."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in replicaIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.AttachDisklessClient(ctx, in.Resource, in.Node); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(in.Node + " attached as a diskless client of " + in.Resource), nil
		})

	addWrite(s, srv, writeTool("sds_resource_detach_diskless", "Detach a diskless client",
		"Stop a node using a resource over the network. Does not touch any stored copy."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in replicaIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.DetachDisklessClient(ctx, in.Resource, in.Node); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(in.Node + " detached from " + in.Resource), nil
		})

	addWrite(s, srv, writeTool("sds_resource_set_tiebreaker", "Set a quorum tiebreaker",
		"Add a diskless node that only votes in quorum and stores no data. A two-replica resource cannot keep "+
			"serving I/O when either node fails, because the survivor has no majority; a tiebreaker fixes that. "+
			"Note that a node attached as a diskless CLIENT does not vote — only a tiebreaker does."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in tiebreakerIn) (*mcp.CallToolResult, opResult, error) {
			msg, _, err := s.client.SetTiebreaker(ctx, in.Resource, in.Node)
			if err != nil {
				return nil, opResult{}, err
			}
			if msg == "" {
				msg = in.Node + " is now the tiebreaker for " + in.Resource
			}
			return nil, ok(msg), nil
		})

	addWrite(s, srv, writeTool("sds_resource_add_dr", "Add off-site DR replication",
		"Add an asynchronous off-site replica reached over a WAN tunnel. The DR copy replicates under protocol A "+
			"and never takes over automatically."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in addDRIn) (*mcp.CallToolResult, addDROut, error) {
			port, err := s.client.AddDR(ctx, in.Resource, in.DRNode, in.DREndpoint, in.WANPort, in.EgressAddress)
			if err != nil {
				return nil, addDROut{}, err
			}
			return nil, addDROut{Status: "ok", WANPort: port}, nil
		})

	addWrite(s, srv, writeTool("sds_wan_repair", "Repair WAN replication tunnels",
		"Reconcile a WAN resource's tunnels with the controller's current node list: re-provision the legs that "+
			"should exist, remove instances left behind by a node that was renumbered or removed. It converges, so "+
			"running it on a healthy resource does nothing. Always run with dry_run first — a repair restarts tunnels."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in wanRepairIn) (*mcp.CallToolResult, wanRepairOut, error) {
			resp, err := s.client.RepairWanProxy(ctx, in.Resource, in.DryRun)
			if err != nil {
				return nil, wanRepairOut{}, err
			}
			return nil, wanRepairOut{
				Status:            "ok",
				Message:           resp.Message,
				ExpectedLegs:      resp.ExpectedLegs,
				RemovedLegs:       resp.RemovedLegs,
				AlreadyConsistent: resp.AlreadyConsistent,
			}, nil
		})

	if c, supported := s.client.(interface {
		SetWanEndpoint(context.Context, *sdspb.SetWanEndpointRequest) (*sdspb.SetWanEndpointResponse, error)
	}); supported {
		addWrite(s, srv, writeTool("sds_wan_set_endpoint", "Change a WAN resource's DR endpoint",
			"Change the DR site's address a WAN resource's primary dials (IP or host name, no port) and/or the source "+
				"address it dials from, and rebuild the tunnels on it. A new DR endpoint that does not answer is refused "+
				"and the old one kept, unless skip_check is set for a DR site not reachable yet."),
			func(ctx context.Context, _ *mcp.CallToolRequest, in wanEndpointIn) (*mcp.CallToolResult, opResult, error) {
				resp, err := c.SetWanEndpoint(ctx, &sdspb.SetWanEndpointRequest{
					Name: in.Resource, DrEndpoint: in.DREndpoint, EgressAddress: in.EgressAddress,
					ClearEgress: in.ClearEgress, SkipReachabilityCheck: in.SkipCheck,
				})
				if err != nil {
					return nil, opResult{}, err
				}
				return nil, ok(resp.Message), nil
			})
	}

	addWrite(s, srv, destructiveTool("sds_node_drain", "Drain a node",
		"Move every resource off a node so it can be taken out of service. Interrupts whatever was running there."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nodeNameIn) (*mcp.CallToolResult, drainOut, error) {
			moved, err := s.client.DrainNode(ctx, in.Node)
			if err != nil {
				return nil, drainOut{}, err
			}
			return nil, drainOut{Status: "ok", Evacuated: moved}, nil
		})

	addWrite(s, srv, writeTool("sds_node_undrain", "Undrain a node",
		"Mark a drained node eligible to hold resources again. Does not move anything back on its own."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nodeNameIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.UndrainNode(ctx, in.Node); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok("node " + in.Node + " is schedulable again"), nil
		})

	addWrite(s, srv, writeTool("sds_node_set_labels", "Set node labels",
		"Set key/value labels on a node. Labels drive placement — which nodes a resource's replicas may land on."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nodeLabelsIn) (*mcp.CallToolResult, opResult, error) {
			if _, err := s.client.SetNodeLabels(ctx, in.Node, in.Labels, in.Replace); err != nil {
				return nil, opResult{}, err
			}
			verb := "merged into"
			if in.Replace {
				verb = "replaced on"
			}
			return nil, ok(fmt.Sprintf("%d label(s) %s %s", len(in.Labels), verb, in.Node)), nil
		})

	if c, supported := s.client.(interface {
		SetNodeAddress(context.Context, string, string, string) (*sdspb.SetNodeAddressResponse, error)
	}); supported {
		addWrite(s, srv, destructiveTool("sds_node_set_address", "Renumber a node",
			"Move a registered node to a new IP address everywhere SDS records it: the node registry, /etc/hosts on the nodes, "+
				"and the DRBD config of every resource it takes part in, each of which reconnects on the new address. "+
				"The node must already answer on the new address as the same machine."),
			func(ctx context.Context, _ *mcp.CallToolRequest, in nodeAddressIn) (*mcp.CallToolResult, nodeAddressOut, error) {
				resp, err := c.SetNodeAddress(ctx, in.Node, in.Address, in.ReplicationAddress)
				if err != nil {
					return nil, nodeAddressOut{}, err
				}
				return nil, nodeAddressOut{OK: resp.Success, Message: resp.Message, Resources: resp.Resources, Failed: resp.Failed}, nil
			})
	}

	addWrite(s, srv, destructiveTool("sds_pool_convert_thin", "Convert a pool to thin",
		"Rebuild an LVM volume group as a thin pool in place. A thick pool reserves a fixed copy-on-write area per "+
			"snapshot and so cannot hold a snapshot history. Every resource on the pool is rebuilt one copy at a "+
			"time, which reduces redundancy while it runs; it is refused when fewer than two diskful copies remain."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in convertThinIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.ConvertPoolToThin(ctx, in.Node, in.Pool); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok("pool " + in.Pool + " on " + in.Node + " converted to thin"), nil
		})

	addRead(s, srv, readOnlyTool("sds_ha_get_toml", "Read an HA promoter config",
		"Read the drbd-reactor promoter TOML for an HA resource — the file that decides what starts, in what order, "+
			"on whichever node holds the resource."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in haTomlIn) (*mcp.CallToolResult, haTomlOut, error) {
			resp, err := s.client.GetHaToml(ctx, in.Resource)
			if err != nil {
				return nil, haTomlOut{}, err
			}
			return nil, haTomlOut{Resource: in.Resource, Content: resp.Content, Path: resp.Path}, nil
		})

	addWrite(s, srv, destructiveTool("sds_ha_sync_toml", "Write an HA promoter config",
		"Write a promoter TOML to every node and reload drbd-reactor. A malformed file can stop the resource from "+
			"starting anywhere, so read the current one first and change only what is needed."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in haTomlSyncIn) (*mcp.CallToolResult, opResult, error) {
			msg, err := s.client.SyncHaToml(ctx, in.Resource, in.Content)
			if err != nil {
				return nil, opResult{}, err
			}
			if msg == "" {
				msg = "promoter config for " + in.Resource + " synced"
			}
			return nil, ok(msg), nil
		})

	addRead(s, srv, readOnlyTool("sds_ha_promoter_status", "Show promoter status",
		"Show drbd-reactor promoter state per HA resource: where it is Primary and whether its services are up."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in haStatusIn) (*mcp.CallToolResult, haStatusOut, error) {
			sts, err := s.client.GetHaStatus(ctx, in.Resource)
			if err != nil {
				return nil, haStatusOut{}, err
			}
			out := haStatusOut{Promoters: make([]haPromoterOut, 0, len(sts))}
			for _, p := range sts {
				out.Promoters = append(out.Promoters, haPromoterOut{
					Resource:  p.DrbdResource,
					PrimaryOn: p.PrimaryOn,
					Status:    p.Status,
				})
			}
			return nil, out, nil
		})

	addRead(s, srv, readOnlyTool("sds_ocf_agent_list", "List OCF resource agents",
		"List the OCF resource agents installed on the cluster. These are what an HA promoter can start — "+
			"Filesystem, IPaddr2, nfsserver and so on."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, agentListOut, error) {
			agents, err := s.client.ListResourceAgents(ctx)
			if err != nil {
				return nil, agentListOut{}, err
			}
			out := agentListOut{Agents: make([]agentOut, 0, len(agents))}
			for _, a := range agents {
				out.Agents = append(out.Agents, agentOut{Provider: a.Provider, Name: a.Name, ShortDesc: a.Shortdesc})
			}
			return nil, out, nil
		})

	addRead(s, srv, readOnlyTool("sds_ocf_agent_metadata", "Describe an OCF agent",
		"Fetch an OCF agent's metadata, which lists every parameter it accepts. Read this before composing a "+
			"promoter start list, rather than guessing parameter names."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in agentMetaIn) (*mcp.CallToolResult, agentMetaOut, error) {
			if strings.TrimSpace(in.Name) == "" {
				return nil, agentMetaOut{}, fmt.Errorf("agent name is required")
			}
			resp, err := s.client.GetResourceAgentMetadata(ctx, in.Provider, in.Name)
			if err != nil {
				return nil, agentMetaOut{}, err
			}
			out := agentMetaOut{
				Provider:   resp.Provider,
				Name:       resp.Name,
				Version:    resp.Version,
				ShortDesc:  resp.Shortdesc,
				LongDesc:   resp.Longdesc,
				Parameters: make([]agentParamOut, 0, len(resp.Parameters)),
			}
			for _, p := range resp.Parameters {
				out.Parameters = append(out.Parameters, agentParamOut{
					Name:      p.Name,
					Required:  p.Required,
					Type:      p.Type,
					Default:   p.Default,
					ShortDesc: p.Shortdesc,
				})
			}
			return nil, out, nil
		})
}

type nodeAddressIn struct {
	Node               string `json:"node" jsonschema:"registered node name"`
	Address            string `json:"address" jsonschema:"the node's new IP address"`
	ReplicationAddress string `json:"replication_address,omitempty" jsonschema:"move DRBD traffic to this address too; empty keeps the node's current arrangement"`
}

type nodeAddressOut struct {
	OK        bool     `json:"ok"`
	Message   string   `json:"message"`
	Resources []string `json:"resources"`
	Failed    []string `json:"failed,omitempty"`
}

type wanEndpointIn struct {
	Resource      string `json:"resource" jsonschema:"WAN resource name"`
	DREndpoint    string `json:"dr_endpoint,omitempty" jsonschema:"DR site's address the primary dials: IP or host name, no port; empty keeps it"`
	EgressAddress string `json:"egress_address,omitempty" jsonschema:"source address the primary dials from; empty keeps it"`
	ClearEgress   bool   `json:"clear_egress,omitempty" jsonschema:"let the routing table choose the source address again"`
	SkipCheck     bool   `json:"skip_check,omitempty" jsonschema:"save and provision even if the DR endpoint does not answer yet"`
}
