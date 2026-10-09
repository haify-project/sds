package controller

import (
	"context"
	"strconv"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/gateway"
)

func (s *Server) AddNFSExport(ctx context.Context, req *haifypb.AddNFSExportRequest) (*haifypb.AddNFSExportResponse, error) {
	nfsMgr := gateway.NewNFSManager(s.gateway)
	err := nfsMgr.AddNFSExport(ctx, req.Resource, req.ExportPath, int(req.Fsid), req.ClientSpec, req.Options)
	if err != nil {
		return &haifypb.AddNFSExportResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.AddNFSExportResponse{Success: true, Message: "NFS export added successfully"}, nil
}

func (s *Server) RemoveNFSExport(ctx context.Context, req *haifypb.RemoveNFSExportRequest) (*haifypb.RemoveNFSExportResponse, error) {
	nfsMgr := gateway.NewNFSManager(s.gateway)
	err := nfsMgr.RemoveNFSExport(ctx, req.Resource, req.ExportPath)
	if err != nil {
		return &haifypb.RemoveNFSExportResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.RemoveNFSExportResponse{Success: true, Message: "NFS export removed successfully"}, nil
}

func (s *Server) ListNFSExports(ctx context.Context, req *haifypb.ListNFSExportsRequest) (*haifypb.ListNFSExportsResponse, error) {
	nfsMgr := gateway.NewNFSManager(s.gateway)
	exports, err := nfsMgr.ListNFSExports(ctx, req.Resource)
	if err != nil {
		return &haifypb.ListNFSExportsResponse{Success: false, Message: err.Error()}, nil
	}

	items := make([]*haifypb.NFSExportInfo, 0, len(exports))
	for _, export := range exports {
		items = append(items, &haifypb.NFSExportInfo{
			Directory:  export["directory"],
			Fsid:       export["fsid"],
			Clientspec: export["clientspec"],
			Options:    export["options"],
		})
	}

	return &haifypb.ListNFSExportsResponse{
		Success: true,
		Message: "NFS exports listed successfully",
		Exports: items,
	}, nil
}

func (s *Server) AddISCSILUN(ctx context.Context, req *haifypb.AddISCSILUNRequest) (*haifypb.AddISCSILUNResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.AddLUN(ctx, req.Resource, int(req.Lun), req.Device)
	if err != nil {
		return &haifypb.AddISCSILUNResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.AddISCSILUNResponse{Success: true, Message: "iSCSI LUN added successfully"}, nil
}

func (s *Server) RemoveISCSILUN(ctx context.Context, req *haifypb.RemoveISCSILUNRequest) (*haifypb.RemoveISCSILUNResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.RemoveLUN(ctx, req.Resource, int(req.Lun))
	if err != nil {
		return &haifypb.RemoveISCSILUNResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.RemoveISCSILUNResponse{Success: true, Message: "iSCSI LUN removed successfully"}, nil
}

func (s *Server) ListISCSILUNs(ctx context.Context, req *haifypb.ListISCSILUNsRequest) (*haifypb.ListISCSILUNsResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	luns, err := iscsiMgr.ListLUNs(ctx, req.Resource)
	if err != nil {
		return &haifypb.ListISCSILUNsResponse{Success: false, Message: err.Error()}, nil
	}

	items := make([]*haifypb.ISCSILUNInfo, 0, len(luns))
	for _, lun := range luns {
		lunNumber, _ := strconv.Atoi(lun["lun"])
		items = append(items, &haifypb.ISCSILUNInfo{
			Lun:       int32(lunNumber),
			Device:    lun["device"],
			TargetIqn: lun["target_iqn"],
		})
	}

	return &haifypb.ListISCSILUNsResponse{
		Success: true,
		Message: "iSCSI LUNs listed successfully",
		Luns:    items,
	}, nil
}

func (s *Server) AddISCSIInitiator(ctx context.Context, req *haifypb.AddISCSIInitiatorRequest) (*haifypb.AddISCSIInitiatorResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.AddInitiator(ctx, req.Resource, req.Initiator)
	if err != nil {
		return &haifypb.AddISCSIInitiatorResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.AddISCSIInitiatorResponse{Success: true, Message: "iSCSI initiator added successfully"}, nil
}

func (s *Server) RemoveISCSIInitiator(ctx context.Context, req *haifypb.RemoveISCSIInitiatorRequest) (*haifypb.RemoveISCSIInitiatorResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.RemoveInitiator(ctx, req.Resource, req.Initiator)
	if err != nil {
		return &haifypb.RemoveISCSIInitiatorResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.RemoveISCSIInitiatorResponse{Success: true, Message: "iSCSI initiator removed successfully"}, nil
}

func (s *Server) ListISCSIInitiators(ctx context.Context, req *haifypb.ListISCSIInitiatorsRequest) (*haifypb.ListISCSIInitiatorsResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	initiators, err := iscsiMgr.ListInitiators(ctx, req.Resource)
	if err != nil {
		return &haifypb.ListISCSIInitiatorsResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.ListISCSIInitiatorsResponse{
		Success:    true,
		Message:    "iSCSI initiators listed successfully",
		Initiators: initiators,
	}, nil
}

func (s *Server) SetISCSIChap(ctx context.Context, req *haifypb.SetISCSIChapRequest) (*haifypb.SetISCSIChapResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.SetCHAP(ctx, req.Resource, req.Username, req.Password, req.Mutual)
	if err != nil {
		return &haifypb.SetISCSIChapResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.SetISCSIChapResponse{Success: true, Message: "iSCSI CHAP updated successfully"}, nil
}

func (s *Server) GetISCSIChap(ctx context.Context, req *haifypb.GetISCSIChapRequest) (*haifypb.GetISCSIChapResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	username, password, mutual, err := iscsiMgr.GetCHAP(ctx, req.Resource)
	if err != nil {
		return &haifypb.GetISCSIChapResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.GetISCSIChapResponse{
		Success:  true,
		Message:  "iSCSI CHAP retrieved successfully",
		Username: username,
		Password: password,
		Mutual:   mutual,
	}, nil
}

func (s *Server) AddNVMeNamespace(ctx context.Context, req *haifypb.AddNVMeNamespaceRequest) (*haifypb.AddNVMeNamespaceResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	err := nvmeMgr.AddNamespace(ctx, req.Resource, req.Device)
	if err != nil {
		return &haifypb.AddNVMeNamespaceResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.AddNVMeNamespaceResponse{Success: true, Message: "NVMe namespace added successfully"}, nil
}

func (s *Server) RemoveNVMeNamespace(ctx context.Context, req *haifypb.RemoveNVMeNamespaceRequest) (*haifypb.RemoveNVMeNamespaceResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	err := nvmeMgr.RemoveNamespace(ctx, req.Resource, int(req.NamespaceId))
	if err != nil {
		return &haifypb.RemoveNVMeNamespaceResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.RemoveNVMeNamespaceResponse{Success: true, Message: "NVMe namespace removed successfully"}, nil
}

func (s *Server) ListNVMeNamespaces(ctx context.Context, req *haifypb.ListNVMeNamespacesRequest) (*haifypb.ListNVMeNamespacesResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	namespaces, err := nvmeMgr.ListNamespaces(ctx, req.Resource)
	if err != nil {
		return &haifypb.ListNVMeNamespacesResponse{Success: false, Message: err.Error()}, nil
	}

	items := make([]*haifypb.NVMeNamespaceInfo, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespaceID, _ := strconv.Atoi(namespace["namespace_id"])
		items = append(items, &haifypb.NVMeNamespaceInfo{
			NamespaceId: int32(namespaceID),
			BackingPath: namespace["backing_path"],
			Uuid:        namespace["uuid"],
			Nguid:       namespace["nguid"],
			Nqn:         namespace["nqn"],
		})
	}

	return &haifypb.ListNVMeNamespacesResponse{
		Success:    true,
		Message:    "NVMe namespaces listed successfully",
		Namespaces: items,
	}, nil
}

func (s *Server) AddNVMeHost(ctx context.Context, req *haifypb.AddNVMeHostRequest) (*haifypb.AddNVMeHostResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	err := nvmeMgr.AddHost(ctx, req.Resource, req.HostNqn)
	if err != nil {
		return &haifypb.AddNVMeHostResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.AddNVMeHostResponse{Success: true, Message: "NVMe host added successfully"}, nil
}

func (s *Server) RemoveNVMeHost(ctx context.Context, req *haifypb.RemoveNVMeHostRequest) (*haifypb.RemoveNVMeHostResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	err := nvmeMgr.RemoveHost(ctx, req.Resource, req.HostNqn)
	if err != nil {
		return &haifypb.RemoveNVMeHostResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.RemoveNVMeHostResponse{Success: true, Message: "NVMe host removed successfully"}, nil
}

func (s *Server) ListNVMeHosts(ctx context.Context, req *haifypb.ListNVMeHostsRequest) (*haifypb.ListNVMeHostsResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	hosts, err := nvmeMgr.ListHosts(ctx, req.Resource)
	if err != nil {
		return &haifypb.ListNVMeHostsResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.ListNVMeHostsResponse{
		Success: true,
		Message: "NVMe hosts listed successfully",
		Hosts:   hosts,
	}, nil
}
