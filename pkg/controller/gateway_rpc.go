package controller

import (
	"context"
	"strconv"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/gateway"
)

func (s *Server) AddNFSExport(ctx context.Context, req *sdspb.AddNFSExportRequest) (*sdspb.AddNFSExportResponse, error) {
	nfsMgr := gateway.NewNFSManager(s.gateway)
	err := nfsMgr.AddNFSExport(ctx, req.Resource, req.ExportPath, int(req.Fsid), req.ClientSpec, req.Options)
	if err != nil {
		return &sdspb.AddNFSExportResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.AddNFSExportResponse{Success: true, Message: "NFS export added successfully"}, nil
}

func (s *Server) RemoveNFSExport(ctx context.Context, req *sdspb.RemoveNFSExportRequest) (*sdspb.RemoveNFSExportResponse, error) {
	nfsMgr := gateway.NewNFSManager(s.gateway)
	err := nfsMgr.RemoveNFSExport(ctx, req.Resource, req.ExportPath)
	if err != nil {
		return &sdspb.RemoveNFSExportResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.RemoveNFSExportResponse{Success: true, Message: "NFS export removed successfully"}, nil
}

func (s *Server) ListNFSExports(ctx context.Context, req *sdspb.ListNFSExportsRequest) (*sdspb.ListNFSExportsResponse, error) {
	nfsMgr := gateway.NewNFSManager(s.gateway)
	exports, err := nfsMgr.ListNFSExports(ctx, req.Resource)
	if err != nil {
		return &sdspb.ListNFSExportsResponse{Success: false, Message: err.Error()}, nil
	}

	items := make([]*sdspb.NFSExportInfo, 0, len(exports))
	for _, export := range exports {
		items = append(items, &sdspb.NFSExportInfo{
			Directory:  export["directory"],
			Fsid:       export["fsid"],
			Clientspec: export["clientspec"],
			Options:    export["options"],
		})
	}

	return &sdspb.ListNFSExportsResponse{
		Success: true,
		Message: "NFS exports listed successfully",
		Exports: items,
	}, nil
}

func (s *Server) AddISCSILUN(ctx context.Context, req *sdspb.AddISCSILUNRequest) (*sdspb.AddISCSILUNResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.AddLUN(ctx, req.Resource, int(req.Lun), req.Device)
	if err != nil {
		return &sdspb.AddISCSILUNResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.AddISCSILUNResponse{Success: true, Message: "iSCSI LUN added successfully"}, nil
}

func (s *Server) RemoveISCSILUN(ctx context.Context, req *sdspb.RemoveISCSILUNRequest) (*sdspb.RemoveISCSILUNResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.RemoveLUN(ctx, req.Resource, int(req.Lun))
	if err != nil {
		return &sdspb.RemoveISCSILUNResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.RemoveISCSILUNResponse{Success: true, Message: "iSCSI LUN removed successfully"}, nil
}

func (s *Server) ListISCSILUNs(ctx context.Context, req *sdspb.ListISCSILUNsRequest) (*sdspb.ListISCSILUNsResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	luns, err := iscsiMgr.ListLUNs(ctx, req.Resource)
	if err != nil {
		return &sdspb.ListISCSILUNsResponse{Success: false, Message: err.Error()}, nil
	}

	items := make([]*sdspb.ISCSILUNInfo, 0, len(luns))
	for _, lun := range luns {
		lunNumber, _ := strconv.Atoi(lun["lun"])
		items = append(items, &sdspb.ISCSILUNInfo{
			Lun:       int32(lunNumber),
			Device:    lun["device"],
			TargetIqn: lun["target_iqn"],
		})
	}

	return &sdspb.ListISCSILUNsResponse{
		Success: true,
		Message: "iSCSI LUNs listed successfully",
		Luns:    items,
	}, nil
}

func (s *Server) AddISCSIInitiator(ctx context.Context, req *sdspb.AddISCSIInitiatorRequest) (*sdspb.AddISCSIInitiatorResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.AddInitiator(ctx, req.Resource, req.Initiator)
	if err != nil {
		return &sdspb.AddISCSIInitiatorResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.AddISCSIInitiatorResponse{Success: true, Message: "iSCSI initiator added successfully"}, nil
}

func (s *Server) RemoveISCSIInitiator(ctx context.Context, req *sdspb.RemoveISCSIInitiatorRequest) (*sdspb.RemoveISCSIInitiatorResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.RemoveInitiator(ctx, req.Resource, req.Initiator)
	if err != nil {
		return &sdspb.RemoveISCSIInitiatorResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.RemoveISCSIInitiatorResponse{Success: true, Message: "iSCSI initiator removed successfully"}, nil
}

func (s *Server) ListISCSIInitiators(ctx context.Context, req *sdspb.ListISCSIInitiatorsRequest) (*sdspb.ListISCSIInitiatorsResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	initiators, err := iscsiMgr.ListInitiators(ctx, req.Resource)
	if err != nil {
		return &sdspb.ListISCSIInitiatorsResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.ListISCSIInitiatorsResponse{
		Success:    true,
		Message:    "iSCSI initiators listed successfully",
		Initiators: initiators,
	}, nil
}

func (s *Server) SetISCSIChap(ctx context.Context, req *sdspb.SetISCSIChapRequest) (*sdspb.SetISCSIChapResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	err := iscsiMgr.SetCHAP(ctx, req.Resource, req.Username, req.Password, req.Mutual)
	if err != nil {
		return &sdspb.SetISCSIChapResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.SetISCSIChapResponse{Success: true, Message: "iSCSI CHAP updated successfully"}, nil
}

func (s *Server) GetISCSIChap(ctx context.Context, req *sdspb.GetISCSIChapRequest) (*sdspb.GetISCSIChapResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	username, password, mutual, err := iscsiMgr.GetCHAP(ctx, req.Resource)
	if err != nil {
		return &sdspb.GetISCSIChapResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.GetISCSIChapResponse{
		Success:  true,
		Message:  "iSCSI CHAP retrieved successfully",
		Username: username,
		Password: password,
		Mutual:   mutual,
	}, nil
}

func (s *Server) AddNVMeNamespace(ctx context.Context, req *sdspb.AddNVMeNamespaceRequest) (*sdspb.AddNVMeNamespaceResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	err := nvmeMgr.AddNamespace(ctx, req.Resource, req.Device)
	if err != nil {
		return &sdspb.AddNVMeNamespaceResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.AddNVMeNamespaceResponse{Success: true, Message: "NVMe namespace added successfully"}, nil
}

func (s *Server) RemoveNVMeNamespace(ctx context.Context, req *sdspb.RemoveNVMeNamespaceRequest) (*sdspb.RemoveNVMeNamespaceResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	err := nvmeMgr.RemoveNamespace(ctx, req.Resource, int(req.NamespaceId))
	if err != nil {
		return &sdspb.RemoveNVMeNamespaceResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.RemoveNVMeNamespaceResponse{Success: true, Message: "NVMe namespace removed successfully"}, nil
}

func (s *Server) ListNVMeNamespaces(ctx context.Context, req *sdspb.ListNVMeNamespacesRequest) (*sdspb.ListNVMeNamespacesResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	namespaces, err := nvmeMgr.ListNamespaces(ctx, req.Resource)
	if err != nil {
		return &sdspb.ListNVMeNamespacesResponse{Success: false, Message: err.Error()}, nil
	}

	items := make([]*sdspb.NVMeNamespaceInfo, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespaceID, _ := strconv.Atoi(namespace["namespace_id"])
		items = append(items, &sdspb.NVMeNamespaceInfo{
			NamespaceId: int32(namespaceID),
			BackingPath: namespace["backing_path"],
			Uuid:        namespace["uuid"],
			Nguid:       namespace["nguid"],
			Nqn:         namespace["nqn"],
		})
	}

	return &sdspb.ListNVMeNamespacesResponse{
		Success:    true,
		Message:    "NVMe namespaces listed successfully",
		Namespaces: items,
	}, nil
}

func (s *Server) AddNVMeHost(ctx context.Context, req *sdspb.AddNVMeHostRequest) (*sdspb.AddNVMeHostResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	err := nvmeMgr.AddHost(ctx, req.Resource, req.HostNqn)
	if err != nil {
		return &sdspb.AddNVMeHostResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.AddNVMeHostResponse{Success: true, Message: "NVMe host added successfully"}, nil
}

func (s *Server) RemoveNVMeHost(ctx context.Context, req *sdspb.RemoveNVMeHostRequest) (*sdspb.RemoveNVMeHostResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	err := nvmeMgr.RemoveHost(ctx, req.Resource, req.HostNqn)
	if err != nil {
		return &sdspb.RemoveNVMeHostResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.RemoveNVMeHostResponse{Success: true, Message: "NVMe host removed successfully"}, nil
}

func (s *Server) ListNVMeHosts(ctx context.Context, req *sdspb.ListNVMeHostsRequest) (*sdspb.ListNVMeHostsResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	hosts, err := nvmeMgr.ListHosts(ctx, req.Resource)
	if err != nil {
		return &sdspb.ListNVMeHostsResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.ListNVMeHostsResponse{
		Success: true,
		Message: "NVMe hosts listed successfully",
		Hosts:   hosts,
	}, nil
}
