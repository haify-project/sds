package mcpserver

import (
	"context"
	"fmt"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- output types ----

type gatewayOut struct {
	Resource string            `json:"resource"`
	Type     string            `json:"type" jsonschema:"gateway type: nfs, iscsi, or nvmeof"`
	State    string            `json:"state,omitempty"`
	Node     string            `json:"node,omitempty" jsonschema:"node currently serving the gateway"`
	Path     string            `json:"path,omitempty"`
	Options  map[string]string `json:"options,omitempty" jsonschema:"type-specific details (IQN/NQN, service IP, exports, ...)"`
}

type gatewayListOut struct {
	Gateways []gatewayOut `json:"gateways"`
}

func gatewayInfoOut(g *sdspb.GatewayInfo) gatewayOut {
	return gatewayOut{
		Resource: g.Resource,
		Type:     g.Type,
		State:    g.State,
		Node:     g.Node,
		Path:     g.Path,
		Options:  g.Options,
	}
}

// ---- input types ----

type gatewayResourceIn struct {
	Resource string `json:"resource" jsonschema:"DRBD resource name backing the gateway"`
}

type nfsGatewayCreateIn struct {
	Resource   string   `json:"resource" jsonschema:"existing DRBD resource to export"`
	ServiceIP  string   `json:"service_ip" jsonschema:"floating service IP in CIDR notation, e.g. 192.168.1.200/24"`
	ExportPath string   `json:"export_path" jsonschema:"directory to export, e.g. /data (used verbatim)"`
	AllowedIPs []string `json:"allowed_ips,omitempty" jsonschema:"client networks allowed to mount, e.g. [\"192.168.1.0/24\"]; default allows all"`
	FsType     string   `json:"fs_type,omitempty" jsonschema:"filesystem type on the volume: ext4 (default) or xfs"`
}

type iscsiGatewayCreateIn struct {
	Resource          string   `json:"resource" jsonschema:"existing DRBD resource to export"`
	IQN               string   `json:"iqn" jsonschema:"iSCSI Qualified Name, e.g. iqn.2024-01.com.example:sds.data"`
	ServiceIP         string   `json:"service_ip" jsonschema:"floating service IP in CIDR notation, e.g. 192.168.1.100/24"`
	AllowedInitiators []string `json:"allowed_initiators,omitempty" jsonschema:"initiator IQNs allowed to connect; default allows all"`
	Username          string   `json:"username,omitempty" jsonschema:"CHAP username"`
	Password          string   `json:"password,omitempty" jsonschema:"CHAP password"`
	Implementation    string   `json:"implementation,omitempty" jsonschema:"target implementation: lio (default), tgt, or iet"`
}

type nvmeGatewayCreateIn struct {
	Resource  string `json:"resource" jsonschema:"existing DRBD resource to export"`
	NQN       string `json:"nqn" jsonschema:"NVMe Qualified Name, e.g. nqn.2024-01.com.example:sds.data"`
	ServiceIP string `json:"service_ip" jsonschema:"floating service IP in CIDR notation, e.g. 192.168.1.150/24"`
	Transport string `json:"transport,omitempty" jsonschema:"transport type: tcp (default) or rdma"`
}

type nfsExportsIn struct {
	Action     string `json:"action" jsonschema:"list, add, or remove"`
	Resource   string `json:"resource" jsonschema:"NFS gateway resource name"`
	ExportPath string `json:"export_path,omitempty" jsonschema:"export directory (required for add/remove)"`
	Fsid       int32  `json:"fsid,omitempty" jsonschema:"filesystem ID for the export (add only; auto-assigned when 0)"`
	ClientSpec string `json:"client_spec,omitempty" jsonschema:"client network spec for add, e.g. 10.0.0.0/8"`
	Options    string `json:"options,omitempty" jsonschema:"NFS export options for add, e.g. rw,no_root_squash"`
}

type nfsExportOut struct {
	Directory  string `json:"directory"`
	Fsid       string `json:"fsid,omitempty"`
	ClientSpec string `json:"client_spec,omitempty"`
	Options    string `json:"options,omitempty"`
}

type nfsExportsOut struct {
	Detail  string         `json:"detail,omitempty"`
	Exports []nfsExportOut `json:"exports,omitempty"`
}

type iscsiLunsIn struct {
	Action   string `json:"action" jsonschema:"list, add, or remove"`
	Resource string `json:"resource" jsonschema:"iSCSI gateway resource name"`
	Lun      int32  `json:"lun,omitempty" jsonschema:"LUN number (required for add/remove)"`
	Device   string `json:"device,omitempty" jsonschema:"backing device path (required for add)"`
}

type iscsiLunOut struct {
	Lun       int32  `json:"lun"`
	Device    string `json:"device"`
	TargetIQN string `json:"target_iqn,omitempty"`
}

type iscsiLunsOut struct {
	Detail string        `json:"detail,omitempty"`
	Luns   []iscsiLunOut `json:"luns,omitempty"`
}

type iscsiInitiatorsIn struct {
	Action   string `json:"action" jsonschema:"list, add, or remove"`
	Resource string `json:"resource" jsonschema:"iSCSI gateway resource name"`
	IQN      string `json:"iqn,omitempty" jsonschema:"initiator IQN (required for add/remove)"`
}

type stringListOut struct {
	Detail string   `json:"detail,omitempty"`
	Items  []string `json:"items,omitempty"`
}

type iscsiChapIn struct {
	Action   string `json:"action" jsonschema:"get or set"`
	Resource string `json:"resource" jsonschema:"iSCSI gateway resource name"`
	Username string `json:"username,omitempty" jsonschema:"CHAP username (required for set)"`
	Password string `json:"password,omitempty" jsonschema:"CHAP password (required for set)"`
	Mutual   bool   `json:"mutual,omitempty" jsonschema:"enable mutual CHAP (set only)"`
}

type iscsiChapOut struct {
	Detail   string `json:"detail,omitempty"`
	Username string `json:"username,omitempty"`
	Mutual   bool   `json:"mutual,omitempty"`
}

type nvmeNamespacesIn struct {
	Action      string `json:"action" jsonschema:"list, add, or remove"`
	Resource    string `json:"resource" jsonschema:"NVMe-oF gateway resource name"`
	Device      string `json:"device,omitempty" jsonschema:"backing device path (required for add)"`
	NamespaceID int32  `json:"namespace_id,omitempty" jsonschema:"namespace ID (required for remove)"`
}

type nvmeNamespaceOut struct {
	NamespaceID int32  `json:"namespace_id"`
	BackingPath string `json:"backing_path,omitempty"`
	UUID        string `json:"uuid,omitempty"`
	NQN         string `json:"nqn,omitempty"`
}

type nvmeNamespacesOut struct {
	Detail     string             `json:"detail,omitempty"`
	Namespaces []nvmeNamespaceOut `json:"namespaces,omitempty"`
}

type nvmeHostsIn struct {
	Action   string `json:"action" jsonschema:"list, add, or remove"`
	Resource string `json:"resource" jsonschema:"NVMe-oF gateway resource name"`
	HostNQN  string `json:"host_nqn,omitempty" jsonschema:"host NQN (required for add/remove)"`
}

func badAction(action string, allowed string) error {
	return fmt.Errorf("invalid action %q (use %s)", action, allowed)
}

// registerGatewayTools adds gateway lifecycle and per-protocol config tools.
func (s *Server) registerGatewayTools(srv *mcp.Server) {
	s.registerGatewayLifecycle(srv)
	s.registerNFSConfig(srv)
	s.registerISCSIConfig(srv)
	s.registerNVMeConfig(srv)
}

func (s *Server) registerGatewayLifecycle(srv *mcp.Server) {
	addRead(s, srv, readOnlyTool("sds_gateway_list", "List gateways",
		"List all storage gateways (NFS, iSCSI, NVMe-oF) with their type, state, and serving node."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, gatewayListOut, error) {
			gws, err := s.client.ListGateways(ctx)
			if err != nil {
				return nil, gatewayListOut{}, err
			}
			out := gatewayListOut{Gateways: make([]gatewayOut, 0, len(gws))}
			for _, g := range gws {
				out.Gateways = append(out.Gateways, gatewayInfoOut(g))
			}
			return nil, out, nil
		})

	addRead(s, srv, readOnlyTool("sds_gateway_get", "Gateway details",
		"Get full details and live status of one gateway: state, serving node, service IP, exports/targets."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, gatewayOut, error) {
			g, err := s.client.GetGateway(ctx, in.Resource)
			if err != nil {
				return nil, gatewayOut{}, err
			}
			return nil, gatewayInfoOut(g), nil
		})

	addWrite(s, srv, writeTool("sds_gateway_create_nfs", "Create NFS gateway",
		"Export a DRBD resource over NFS with automatic failover. Creates a drbd-reactor promoter config "+
			"with filesystem mount, floating IP, NFS server, and exports. The resource must already exist."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nfsGatewayCreateIn) (*mcp.CallToolResult, opResult, error) {
			fsType := in.FsType
			if fsType == "" {
				fsType = "ext4"
			}
			resp, err := s.client.CreateNFSGateway(ctx, &sdspb.CreateNFSGatewayRequest{
				Resource:   in.Resource,
				ServiceIp:  in.ServiceIP,
				ExportPath: in.ExportPath,
				AllowedIps: in.AllowedIPs,
				FsType:     fsType,
			})
			if err != nil {
				return nil, opResult{}, err
			}
			if !resp.Success {
				return nil, opResult{}, fmt.Errorf("create NFS gateway: %s", resp.Message)
			}
			return nil, ok(fmt.Sprintf("NFS gateway for %s created at %s exporting %s (config %s)",
				in.Resource, in.ServiceIP, in.ExportPath, resp.ConfigPath)), nil
		})

	addWrite(s, srv, writeTool("sds_gateway_create_iscsi", "Create iSCSI gateway",
		"Export a DRBD resource as an iSCSI target with automatic failover. The resource must already exist."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in iscsiGatewayCreateIn) (*mcp.CallToolResult, opResult, error) {
			impl := in.Implementation
			if impl == "" {
				impl = "lio"
			}
			resp, err := s.client.CreateISCSIGateway(ctx, &sdspb.CreateISCSIGatewayRequest{
				Resource:          in.Resource,
				ServiceIp:         in.ServiceIP,
				Iqn:               in.IQN,
				AllowedInitiators: in.AllowedInitiators,
				Username:          in.Username,
				Password:          in.Password,
				Implementation:    impl,
			})
			if err != nil {
				return nil, opResult{}, err
			}
			if !resp.Success {
				return nil, opResult{}, fmt.Errorf("create iSCSI gateway: %s", resp.Message)
			}
			return nil, ok(fmt.Sprintf("iSCSI gateway for %s created at %s with IQN %s",
				in.Resource, in.ServiceIP, in.IQN)), nil
		})

	addWrite(s, srv, writeTool("sds_gateway_create_nvme", "Create NVMe-oF gateway",
		"Export a DRBD resource as an NVMe-oF subsystem with automatic failover. The resource must already exist."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nvmeGatewayCreateIn) (*mcp.CallToolResult, opResult, error) {
			transport := in.Transport
			if transport == "" {
				transport = "tcp"
			}
			resp, err := s.client.CreateNVMeGateway(ctx, &sdspb.CreateNVMeGatewayRequest{
				Resource:      in.Resource,
				ServiceIp:     in.ServiceIP,
				Nqn:           in.NQN,
				TransportType: transport,
			})
			if err != nil {
				return nil, opResult{}, err
			}
			if !resp.Success {
				return nil, opResult{}, fmt.Errorf("create NVMe-oF gateway: %s", resp.Message)
			}
			return nil, ok(fmt.Sprintf("NVMe-oF gateway for %s created at %s with NQN %s",
				in.Resource, in.ServiceIP, in.NQN)), nil
		})

	addWrite(s, srv, writeTool("sds_gateway_start", "Start gateway",
		"Start a stopped gateway: enables its drbd-reactor config and brings services up."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.StartGateway(ctx, in.Resource); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("gateway %s started", in.Resource)), nil
		})

	addWrite(s, srv, destructiveTool("sds_gateway_stop", "Stop gateway",
		"Stop a running gateway: clients are disconnected until it is started again."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.StopGateway(ctx, in.Resource); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("gateway %s stopped", in.Resource)), nil
		})

	addWrite(s, srv, destructiveTool("sds_gateway_delete", "Delete gateway",
		"Delete a gateway: removes the drbd-reactor config and stops the export. "+
			"The underlying DRBD resource and its data are kept."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, opResult, error) {
			if err := s.client.DeleteGateway(ctx, in.Resource); err != nil {
				return nil, opResult{}, err
			}
			return nil, ok(fmt.Sprintf("gateway %s deleted", in.Resource)), nil
		})
}

func (s *Server) registerNFSConfig(srv *mcp.Server) {
	addWrite(s, srv, destructiveTool("sds_nfs_exports", "Manage NFS exports",
		"List, add, or remove NFS exports on an existing NFS gateway."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nfsExportsIn) (*mcp.CallToolResult, nfsExportsOut, error) {
			switch in.Action {
			case "list":
				exports, err := s.client.ListNFSExports(ctx, in.Resource)
				if err != nil {
					return nil, nfsExportsOut{}, err
				}
				out := nfsExportsOut{Exports: make([]nfsExportOut, 0, len(exports))}
				for _, e := range exports {
					out.Exports = append(out.Exports, nfsExportOut{
						Directory:  e.Directory,
						Fsid:       e.Fsid,
						ClientSpec: e.Clientspec,
						Options:    e.Options,
					})
				}
				return nil, out, nil
			case "add":
				if in.ExportPath == "" {
					return nil, nfsExportsOut{}, fmt.Errorf("export_path is required for add")
				}
				if err := s.client.AddNFSExport(ctx, in.Resource, in.ExportPath, in.Fsid, in.ClientSpec, in.Options); err != nil {
					return nil, nfsExportsOut{}, err
				}
				return nil, nfsExportsOut{Detail: fmt.Sprintf("export %s added to gateway %s", in.ExportPath, in.Resource)}, nil
			case "remove":
				if in.ExportPath == "" {
					return nil, nfsExportsOut{}, fmt.Errorf("export_path is required for remove")
				}
				if err := s.client.RemoveNFSExport(ctx, in.Resource, in.ExportPath); err != nil {
					return nil, nfsExportsOut{}, err
				}
				return nil, nfsExportsOut{Detail: fmt.Sprintf("export %s removed from gateway %s", in.ExportPath, in.Resource)}, nil
			default:
				return nil, nfsExportsOut{}, badAction(in.Action, "list, add, or remove")
			}
		})
}

func (s *Server) registerISCSIConfig(srv *mcp.Server) {
	addWrite(s, srv, destructiveTool("sds_iscsi_luns", "Manage iSCSI LUNs",
		"List, add, or remove LUNs on an existing iSCSI gateway."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in iscsiLunsIn) (*mcp.CallToolResult, iscsiLunsOut, error) {
			switch in.Action {
			case "list":
				luns, err := s.client.ListISCSILUNs(ctx, in.Resource)
				if err != nil {
					return nil, iscsiLunsOut{}, err
				}
				out := iscsiLunsOut{Luns: make([]iscsiLunOut, 0, len(luns))}
				for _, l := range luns {
					out.Luns = append(out.Luns, iscsiLunOut{Lun: l.Lun, Device: l.Device, TargetIQN: l.TargetIqn})
				}
				return nil, out, nil
			case "add":
				if in.Device == "" {
					return nil, iscsiLunsOut{}, fmt.Errorf("device is required for add")
				}
				if err := s.client.AddISCSILUN(ctx, in.Resource, in.Lun, in.Device); err != nil {
					return nil, iscsiLunsOut{}, err
				}
				return nil, iscsiLunsOut{Detail: fmt.Sprintf("LUN %d (%s) added to gateway %s", in.Lun, in.Device, in.Resource)}, nil
			case "remove":
				if err := s.client.RemoveISCSILUN(ctx, in.Resource, in.Lun); err != nil {
					return nil, iscsiLunsOut{}, err
				}
				return nil, iscsiLunsOut{Detail: fmt.Sprintf("LUN %d removed from gateway %s", in.Lun, in.Resource)}, nil
			default:
				return nil, iscsiLunsOut{}, badAction(in.Action, "list, add, or remove")
			}
		})

	addWrite(s, srv, destructiveTool("sds_iscsi_initiators", "Manage iSCSI initiators",
		"List, add, or remove initiator IQNs on an iSCSI gateway's allow-list."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in iscsiInitiatorsIn) (*mcp.CallToolResult, stringListOut, error) {
			switch in.Action {
			case "list":
				initiators, err := s.client.ListISCSIInitiators(ctx, in.Resource)
				if err != nil {
					return nil, stringListOut{}, err
				}
				return nil, stringListOut{Items: initiators}, nil
			case "add":
				if in.IQN == "" {
					return nil, stringListOut{}, fmt.Errorf("iqn is required for add")
				}
				if err := s.client.AddISCSIInitiator(ctx, in.Resource, in.IQN); err != nil {
					return nil, stringListOut{}, err
				}
				return nil, stringListOut{Detail: fmt.Sprintf("initiator %s allowed on gateway %s", in.IQN, in.Resource)}, nil
			case "remove":
				if in.IQN == "" {
					return nil, stringListOut{}, fmt.Errorf("iqn is required for remove")
				}
				if err := s.client.RemoveISCSIInitiator(ctx, in.Resource, in.IQN); err != nil {
					return nil, stringListOut{}, err
				}
				return nil, stringListOut{Detail: fmt.Sprintf("initiator %s removed from gateway %s", in.IQN, in.Resource)}, nil
			default:
				return nil, stringListOut{}, badAction(in.Action, "list, add, or remove")
			}
		})

	addWrite(s, srv, writeTool("sds_iscsi_chap", "Manage iSCSI CHAP",
		"Get or set CHAP authentication credentials on an iSCSI gateway. Passwords are never returned."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in iscsiChapIn) (*mcp.CallToolResult, iscsiChapOut, error) {
			switch in.Action {
			case "get":
				chap, err := s.client.GetISCSIChap(ctx, in.Resource)
				if err != nil {
					return nil, iscsiChapOut{}, err
				}
				return nil, iscsiChapOut{Username: chap.Username, Mutual: chap.Mutual}, nil
			case "set":
				if in.Username == "" || in.Password == "" {
					return nil, iscsiChapOut{}, fmt.Errorf("username and password are required for set")
				}
				if err := s.client.SetISCSIChap(ctx, in.Resource, in.Username, in.Password, in.Mutual); err != nil {
					return nil, iscsiChapOut{}, err
				}
				return nil, iscsiChapOut{Detail: fmt.Sprintf("CHAP credentials set on gateway %s", in.Resource)}, nil
			default:
				return nil, iscsiChapOut{}, badAction(in.Action, "get or set")
			}
		})
}

func (s *Server) registerNVMeConfig(srv *mcp.Server) {
	addWrite(s, srv, destructiveTool("sds_nvme_namespaces", "Manage NVMe namespaces",
		"List, add, or remove namespaces on an existing NVMe-oF gateway."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nvmeNamespacesIn) (*mcp.CallToolResult, nvmeNamespacesOut, error) {
			switch in.Action {
			case "list":
				namespaces, err := s.client.ListNVMeNamespaces(ctx, in.Resource)
				if err != nil {
					return nil, nvmeNamespacesOut{}, err
				}
				out := nvmeNamespacesOut{Namespaces: make([]nvmeNamespaceOut, 0, len(namespaces))}
				for _, ns := range namespaces {
					out.Namespaces = append(out.Namespaces, nvmeNamespaceOut{
						NamespaceID: ns.NamespaceId,
						BackingPath: ns.BackingPath,
						UUID:        ns.Uuid,
						NQN:         ns.Nqn,
					})
				}
				return nil, out, nil
			case "add":
				if in.Device == "" {
					return nil, nvmeNamespacesOut{}, fmt.Errorf("device is required for add")
				}
				if err := s.client.AddNVMeNamespace(ctx, in.Resource, in.Device); err != nil {
					return nil, nvmeNamespacesOut{}, err
				}
				return nil, nvmeNamespacesOut{Detail: fmt.Sprintf("namespace %s added to gateway %s", in.Device, in.Resource)}, nil
			case "remove":
				if err := s.client.RemoveNVMeNamespace(ctx, in.Resource, in.NamespaceID); err != nil {
					return nil, nvmeNamespacesOut{}, err
				}
				return nil, nvmeNamespacesOut{Detail: fmt.Sprintf("namespace %d removed from gateway %s", in.NamespaceID, in.Resource)}, nil
			default:
				return nil, nvmeNamespacesOut{}, badAction(in.Action, "list, add, or remove")
			}
		})

	addWrite(s, srv, destructiveTool("sds_nvme_hosts", "Manage NVMe hosts",
		"List, add, or remove host NQNs on an NVMe-oF gateway's allow-list."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nvmeHostsIn) (*mcp.CallToolResult, stringListOut, error) {
			switch in.Action {
			case "list":
				hosts, err := s.client.ListNVMeHosts(ctx, in.Resource)
				if err != nil {
					return nil, stringListOut{}, err
				}
				return nil, stringListOut{Items: hosts}, nil
			case "add":
				if in.HostNQN == "" {
					return nil, stringListOut{}, fmt.Errorf("host_nqn is required for add")
				}
				if err := s.client.AddNVMeHost(ctx, in.Resource, in.HostNQN); err != nil {
					return nil, stringListOut{}, err
				}
				return nil, stringListOut{Detail: fmt.Sprintf("host %s allowed on gateway %s", in.HostNQN, in.Resource)}, nil
			case "remove":
				if in.HostNQN == "" {
					return nil, stringListOut{}, fmt.Errorf("host_nqn is required for remove")
				}
				if err := s.client.RemoveNVMeHost(ctx, in.Resource, in.HostNQN); err != nil {
					return nil, stringListOut{}, err
				}
				return nil, stringListOut{Detail: fmt.Sprintf("host %s removed from gateway %s", in.HostNQN, in.Resource)}, nil
			default:
				return nil, stringListOut{}, badAction(in.Action, "list, add, or remove")
			}
		})
}
