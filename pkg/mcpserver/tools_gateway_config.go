package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerGatewayConfigReads adds read-only views of each gateway's exports,
// LUNs, namespaces and allow-lists. The add/remove tools below also answer
// "list" for clients that already call them that way, but they are
// destructive and so absent for read and operate tokens; these are not.
func (s *Server) registerGatewayConfigReads(srv *mcp.Server) {
	addRead(s, srv, readOnlyTool("sds_nfs_export_list", "List NFS exports",
		"List the exports of an NFS gateway: directory, fsid, client spec and options."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, nfsExportsOut, error) {
			out, err := s.nfsExportList(ctx, in.Resource)
			return nil, out, err
		})
	addRead(s, srv, readOnlyTool("sds_iscsi_lun_list", "List iSCSI LUNs",
		"List the LUNs of an iSCSI gateway and the device behind each."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, iscsiLunsOut, error) {
			out, err := s.iscsiLunList(ctx, in.Resource)
			return nil, out, err
		})
	addRead(s, srv, readOnlyTool("sds_iscsi_initiator_list", "List iSCSI initiators",
		"List the initiator IQNs on an iSCSI gateway's allow-list."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, stringListOut, error) {
			out, err := s.iscsiInitiatorList(ctx, in.Resource)
			return nil, out, err
		})
	addRead(s, srv, readOnlyTool("sds_iscsi_chap_get", "Show iSCSI CHAP settings",
		"Whether an iSCSI gateway requires one-way CHAP, and its username. The password is never returned."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, iscsiChapOut, error) {
			out, err := s.iscsiChapGet(ctx, in.Resource)
			return nil, out, err
		})
	addRead(s, srv, readOnlyTool("sds_nvme_namespace_list", "List NVMe namespaces",
		"List the namespaces of an NVMe-oF gateway: ID, backing device, UUID and NQN."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, nvmeNamespacesOut, error) {
			out, err := s.nvmeNamespaceList(ctx, in.Resource)
			return nil, out, err
		})
	addRead(s, srv, readOnlyTool("sds_nvme_host_list", "List NVMe hosts",
		"List the host NQNs on an NVMe-oF gateway's allow-list."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in gatewayResourceIn) (*mcp.CallToolResult, stringListOut, error) {
			out, err := s.nvmeHostList(ctx, in.Resource)
			return nil, out, err
		})
}

func (s *Server) nfsExportList(ctx context.Context, resource string) (nfsExportsOut, error) {
	exports, err := s.client.ListNFSExports(ctx, resource)
	if err != nil {
		return nfsExportsOut{}, err
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
	return out, nil
}

func (s *Server) iscsiLunList(ctx context.Context, resource string) (iscsiLunsOut, error) {
	luns, err := s.client.ListISCSILUNs(ctx, resource)
	if err != nil {
		return iscsiLunsOut{}, err
	}
	out := iscsiLunsOut{Luns: make([]iscsiLunOut, 0, len(luns))}
	for _, l := range luns {
		out.Luns = append(out.Luns, iscsiLunOut{Lun: l.Lun, Device: l.Device, TargetIQN: l.TargetIqn})
	}
	return out, nil
}

func (s *Server) iscsiInitiatorList(ctx context.Context, resource string) (stringListOut, error) {
	initiators, err := s.client.ListISCSIInitiators(ctx, resource)
	if err != nil {
		return stringListOut{}, err
	}
	return stringListOut{Items: initiators}, nil
}

func (s *Server) iscsiChapGet(ctx context.Context, resource string) (iscsiChapOut, error) {
	chap, err := s.client.GetISCSIChap(ctx, resource)
	if err != nil {
		return iscsiChapOut{}, err
	}
	return iscsiChapOut{Username: chap.Username}, nil
}

func (s *Server) nvmeNamespaceList(ctx context.Context, resource string) (nvmeNamespacesOut, error) {
	namespaces, err := s.client.ListNVMeNamespaces(ctx, resource)
	if err != nil {
		return nvmeNamespacesOut{}, err
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
	return out, nil
}

func (s *Server) nvmeHostList(ctx context.Context, resource string) (stringListOut, error) {
	hosts, err := s.client.ListNVMeHosts(ctx, resource)
	if err != nil {
		return stringListOut{}, err
	}
	return stringListOut{Items: hosts}, nil
}

func (s *Server) registerNFSConfig(srv *mcp.Server) {
	addWrite(s, srv, destructiveTool("sds_nfs_exports", "Manage NFS exports",
		"Add or remove NFS exports on an existing NFS gateway. Action list is still accepted; "+
			"sds_nfs_export_list does the same as a read-only tool."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nfsExportsIn) (*mcp.CallToolResult, nfsExportsOut, error) {
			switch in.Action {
			case "list":
				out, err := s.nfsExportList(ctx, in.Resource)
				return nil, out, err
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
		"Add or remove LUNs on an existing iSCSI gateway. Action list is still accepted; "+
			"sds_iscsi_lun_list does the same as a read-only tool."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in iscsiLunsIn) (*mcp.CallToolResult, iscsiLunsOut, error) {
			switch in.Action {
			case "list":
				out, err := s.iscsiLunList(ctx, in.Resource)
				return nil, out, err
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
		"Add or remove initiator IQNs on an iSCSI gateway's allow-list. Action list is still accepted; "+
			"sds_iscsi_initiator_list does the same as a read-only tool."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in iscsiInitiatorsIn) (*mcp.CallToolResult, stringListOut, error) {
			switch in.Action {
			case "list":
				out, err := s.iscsiInitiatorList(ctx, in.Resource)
				return nil, out, err
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
		"Set one-way CHAP credentials on an iSCSI gateway (mutual CHAP is not supported). Action get is still accepted; "+
			"sds_iscsi_chap_get does the same as a read-only tool. Passwords are never returned."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in iscsiChapIn) (*mcp.CallToolResult, iscsiChapOut, error) {
			switch in.Action {
			case "get":
				out, err := s.iscsiChapGet(ctx, in.Resource)
				return nil, out, err
			case "set":
				if in.Username == "" || in.Password == "" {
					return nil, iscsiChapOut{}, fmt.Errorf("username and password are required for set")
				}
				if err := s.client.SetISCSIChap(ctx, in.Resource, in.Username, in.Password, false); err != nil {
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
		"Add or remove namespaces on an existing NVMe-oF gateway. Action list is still accepted; "+
			"sds_nvme_namespace_list does the same as a read-only tool."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nvmeNamespacesIn) (*mcp.CallToolResult, nvmeNamespacesOut, error) {
			switch in.Action {
			case "list":
				out, err := s.nvmeNamespaceList(ctx, in.Resource)
				return nil, out, err
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
		"Add or remove host NQNs on an NVMe-oF gateway's allow-list. Action list is still accepted; "+
			"sds_nvme_host_list does the same as a read-only tool."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in nvmeHostsIn) (*mcp.CallToolResult, stringListOut, error) {
			switch in.Action {
			case "list":
				out, err := s.nvmeHostList(ctx, in.Resource)
				return nil, out, err
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
