package controller

import (
	"context"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/gateway"
	"go.uber.org/zap"
)

// Server implements the SDS controller gRPC service
type Server struct {
	sdspb.UnimplementedSDSControllerServer
	ctrl      *Controller
	logger    *zap.Logger
	storage   *StorageManager
	resources *ResourceManager
	snapshots *SnapshotManager
	nodes     *NodeManager
	gateway   *gateway.Manager
}

// NewServer creates a new gRPC server
func NewServer(ctrl *Controller) *Server {
	return &Server{
		ctrl:      ctrl,
		logger:    ctrl.logger,
		storage:   ctrl.storage,
		resources: ctrl.resources,
		snapshots: ctrl.snapshots,
		nodes:     ctrl.nodes,
		gateway:   ctrl.gateway,
	}
}

// ==================== POOL OPERATIONS ====================

func (s *Server) CreatePool(ctx context.Context, req *sdspb.CreatePoolRequest) (*sdspb.CreatePoolResponse, error) {
	err := s.storage.CreatePool(ctx, req.Name, req.Type, req.Node, req.Disks, req.SizeGb)
	if err != nil {
		return &sdspb.CreatePoolResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreatePoolResponse{
		Success: true,
		Message: "Pool created successfully",
	}, nil
}

func (s *Server) DeletePool(ctx context.Context, req *sdspb.DeletePoolRequest) (*sdspb.DeletePoolResponse, error) {
	err := s.storage.DeletePool(ctx, req.Name, req.Node)
	if err != nil {
		return &sdspb.DeletePoolResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeletePoolResponse{
		Success: true,
		Message: "Pool deleted successfully",
	}, nil
}

func (s *Server) GetPool(ctx context.Context, req *sdspb.GetPoolRequest) (*sdspb.GetPoolResponse, error) {
	pool, err := s.storage.GetPool(ctx, req.Name, req.Node)
	if err != nil {
		return &sdspb.GetPoolResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.GetPoolResponse{
		Success: true,
		Message: "Pool found",
		Pool: &sdspb.PoolInfo{
			Name:    pool.Name,
			Type:    pool.Type,
			Node:    pool.Node,
			TotalGb: pool.TotalGB,
			FreeGb:  pool.FreeGB,
			Devices: pool.Devices,
		},
	}, nil
}

func (s *Server) ListPools(ctx context.Context, req *sdspb.ListPoolsRequest) (*sdspb.ListPoolsResponse, error) {
	pools, err := s.storage.ListPools(ctx)
	if err != nil {
		return &sdspb.ListPoolsResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbPools []*sdspb.PoolInfo
	for _, p := range pools {
		pbPools = append(pbPools, &sdspb.PoolInfo{
			Name:    p.Name,
			Type:    p.Type,
			Node:    p.Node,
			TotalGb: p.TotalGB,
			FreeGb:  p.FreeGB,
			Devices: p.Devices,
		})
	}

	return &sdspb.ListPoolsResponse{
		Success: true,
		Message: "Pools listed successfully",
		Pools:   pbPools,
	}, nil
}

func (s *Server) AddDiskToPool(ctx context.Context, req *sdspb.AddDiskToPoolRequest) (*sdspb.AddDiskToPoolResponse, error) {
	err := s.storage.AddDiskToPool(ctx, req.Pool, req.Disk, req.Node)
	if err != nil {
		return &sdspb.AddDiskToPoolResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.AddDiskToPoolResponse{
		Success: true,
		Message: "Disk added to pool successfully",
	}, nil
}

// ==================== NODE OPERATIONS ====================

func (s *Server) RegisterNode(ctx context.Context, req *sdspb.RegisterNodeRequest) (*sdspb.RegisterNodeResponse, error) {
	node, err := s.nodes.RegisterNode(ctx, req.Name, req.Address)
	if err != nil {
		return &sdspb.RegisterNodeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.RegisterNodeResponse{
		Success: true,
		Message: "Node registered successfully",
		Node: &sdspb.NodeInfo{
			Name:     node.Name,
			Address:  node.Address,
			Hostname: node.Hostname,
			State:    string(node.State),
			LastSeen: node.LastSeen.Unix(),
			Version:  node.Version,
		},
	}, nil
}

func (s *Server) UnregisterNode(ctx context.Context, req *sdspb.UnregisterNodeRequest) (*sdspb.UnregisterNodeResponse, error) {
	err := s.nodes.UnregisterNode(ctx, req.Address)
	if err != nil {
		return &sdspb.UnregisterNodeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.UnregisterNodeResponse{
		Success: true,
		Message: "Node unregistered successfully",
	}, nil
}

func (s *Server) GetNode(ctx context.Context, req *sdspb.GetNodeRequest) (*sdspb.GetNodeResponse, error) {
	node, err := s.nodes.GetNode(ctx, req.Address)
	if err != nil {
		return &sdspb.GetNodeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.GetNodeResponse{
		Success: true,
		Message: "Node found",
		Node: &sdspb.NodeInfo{
			Name:     node.Name,
			Address:  node.Address,
			Hostname: node.Hostname,
			State:    string(node.State),
			LastSeen: node.LastSeen.Unix(),
			Version:  node.Version,
		},
	}, nil
}

func (s *Server) ListNodes(ctx context.Context, req *sdspb.ListNodesRequest) (*sdspb.ListNodesResponse, error) {
	nodes, err := s.nodes.ListNodes(ctx)
	if err != nil {
		return &sdspb.ListNodesResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbNodes []*sdspb.NodeInfo
	for _, n := range nodes {
		pbNodes = append(pbNodes, &sdspb.NodeInfo{
			Name:     n.Name,
			Address:  n.Address,
			Hostname: n.Hostname,
			State:    string(n.State),
			LastSeen: n.LastSeen.Unix(),
			Version:  n.Version,
		})
	}

	return &sdspb.ListNodesResponse{
		Success: true,
		Message: "Nodes listed successfully",
		Nodes:   pbNodes,
	}, nil
}

// ==================== RESOURCE OPERATIONS ====================

func (s *Server) CreateResource(ctx context.Context, req *sdspb.CreateResourceRequest) (*sdspb.CreateResourceResponse, error) {
	err := s.resources.CreateResource(ctx, req.Name, req.Port, req.Nodes, req.Protocol, req.SizeGb, req.Pool)
	if err != nil {
		return &sdspb.CreateResourceResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateResourceResponse{
		Success: true,
		Message: "Resource created successfully",
	}, nil
}

func (s *Server) DeleteResource(ctx context.Context, req *sdspb.DeleteResourceRequest) (*sdspb.DeleteResourceResponse, error) {
	err := s.resources.DeleteResource(ctx, req.Name, true)
	if err != nil {
		return &sdspb.DeleteResourceResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteResourceResponse{
		Success: true,
		Message: "Resource deleted successfully",
	}, nil
}

func (s *Server) GetResource(ctx context.Context, req *sdspb.GetResourceRequest) (*sdspb.GetResourceResponse, error) {
	resource, err := s.resources.GetResource(ctx, req.Name)
	if err != nil {
		return &sdspb.GetResourceResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbVolumes []*sdspb.VolumeInfo
	for _, v := range resource.Volumes {
		pbVolumes = append(pbVolumes, &sdspb.VolumeInfo{
			VolumeId: v.VolumeID,
			Device:   v.Device,
			SizeGb:   v.SizeGB,
		})
	}

	// Build node states map
	nodeStates := make(map[string]*sdspb.NodeResourceState)
	for node, state := range resource.NodeStates {
		nodeStates[node] = &sdspb.NodeResourceState{
			Role:      state.Role,
			DiskState: state.DiskState,
		}
	}

	return &sdspb.GetResourceResponse{
		Success: true,
		Message: "Resource found",
		Resource: &sdspb.ResourceInfo{
			Name:        resource.Name,
			Port:        resource.Port,
			Protocol:    resource.Protocol,
			Nodes:       resource.Nodes,
			Role:        resource.Role,
			Volumes:     pbVolumes,
			NodeStates:  nodeStates,
		},
	}, nil
}

func (s *Server) ListResources(ctx context.Context, req *sdspb.ListResourcesRequest) (*sdspb.ListResourcesResponse, error) {
	resources, err := s.resources.ListResources(ctx)
	if err != nil {
		return &sdspb.ListResourcesResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbResources []*sdspb.ResourceInfo
	for _, r := range resources {
		var pbVolumes []*sdspb.VolumeInfo
		for _, v := range r.Volumes {
			pbVolumes = append(pbVolumes, &sdspb.VolumeInfo{
				VolumeId: v.VolumeID,
				Device:   v.Device,
				SizeGb:   v.SizeGB,
			})
		}
		pbResources = append(pbResources, &sdspb.ResourceInfo{
			Name:     r.Name,
			Port:     r.Port,
			Protocol: r.Protocol,
			Nodes:    r.Nodes,
			Role:     r.Role,
			Volumes:  pbVolumes,
		})
	}

	return &sdspb.ListResourcesResponse{
		Success: true,
		Message: "Resources listed successfully",
		Resources: pbResources,
	}, nil
}

func (s *Server) AddVolume(ctx context.Context, req *sdspb.AddVolumeRequest) (*sdspb.AddVolumeResponse, error) {
	err := s.resources.AddVolume(ctx, req.Resource, req.Volume, req.Pool, req.SizeGb)
	if err != nil {
		return &sdspb.AddVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.AddVolumeResponse{
		Success: true,
		Message: "Volume added successfully",
	}, nil
}

func (s *Server) RemoveVolume(ctx context.Context, req *sdspb.RemoveVolumeRequest) (*sdspb.RemoveVolumeResponse, error) {
	err := s.resources.RemoveVolume(ctx, req.Resource, req.VolumeId)
	if err != nil {
		return &sdspb.RemoveVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.RemoveVolumeResponse{
		Success: true,
		Message: "Volume removed successfully",
	}, nil
}

func (s *Server) ResizeVolume(ctx context.Context, req *sdspb.ResizeVolumeRequest) (*sdspb.ResizeVolumeResponse, error) {
	err := s.resources.ResizeVolume(ctx, req.Resource, req.VolumeId, uint64(req.SizeGb))
	if err != nil {
		return &sdspb.ResizeVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.ResizeVolumeResponse{
		Success: true,
		Message: "Volume resized successfully",
	}, nil
}

func (s *Server) ResourceStatus(ctx context.Context, req *sdspb.ResourceStatusRequest) (*sdspb.ResourceStatusResponse, error) {
	// Get resource detailed status
	resource, err := s.resources.GetResource(ctx, req.Name)
	if err != nil {
		return &sdspb.ResourceStatusResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	// Convert to status format with detailed node states
	status := &sdspb.ResourceStatus{
		Name:     resource.Name,
		Role:     resource.Role,
		Nodes:    resource.Nodes,
		NodeStates: make(map[string]*sdspb.NodeResourceState),
	}

	// Convert node states from endpoint key to hostname key
	for endpoint, nodeState := range resource.NodeStates {
		// Get hostname for this endpoint
		nodeInfo, err := s.nodes.GetNode(ctx, endpoint)
		hostname := endpoint
		if err == nil && nodeInfo.Hostname != "" {
			hostname = nodeInfo.Hostname
		}

		status.NodeStates[hostname] = &sdspb.NodeResourceState{
			Role:             nodeState.Role,
			DiskState:        nodeState.DiskState,
			ReplicationState: nodeState.Replication,
		}
	}

	return &sdspb.ResourceStatusResponse{
		Success: true,
		Message: "Resource status retrieved",
		Status:  status,
	}, nil
}

func (s *Server) SetPrimary(ctx context.Context, req *sdspb.SetPrimaryRequest) (*sdspb.SetPrimaryResponse, error) {
	err := s.resources.SetPrimary(ctx, req.Resource, req.Node, req.Force)
	if err != nil {
		return &sdspb.SetPrimaryResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.SetPrimaryResponse{
		Success: true,
		Message: "Resource set to Primary successfully",
	}, nil
}

func (s *Server) SetSecondary(ctx context.Context, req *sdspb.SetSecondaryRequest) (*sdspb.SetSecondaryResponse, error) {
	err := s.resources.SetSecondary(ctx, req.Resource, req.Node)
	if err != nil {
		return &sdspb.SetSecondaryResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.SetSecondaryResponse{
		Success: true,
		Message: "Resource set to Secondary successfully",
	}, nil
}

func (s *Server) CreateFilesystem(ctx context.Context, req *sdspb.CreateFilesystemRequest) (*sdspb.CreateFilesystemResponse, error) {
	// CreateFilesystem is implemented as part of Mount operation
	// This is a convenience wrapper that only creates filesystem
	err := s.resources.CreateFilesystemOnly(ctx, req.Resource, req.VolumeId, req.Fstype)
	if err != nil {
		return &sdspb.CreateFilesystemResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateFilesystemResponse{
		Success: true,
		Message: "Filesystem created successfully",
	}, nil
}

func (s *Server) MountResource(ctx context.Context, req *sdspb.MountResourceRequest) (*sdspb.MountResourceResponse, error) {
	err := s.resources.Mount(ctx, req.Resource, req.Path, req.VolumeId, req.Fstype)
	if err != nil {
		return &sdspb.MountResourceResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.MountResourceResponse{
		Success: true,
		Message: "Resource mounted successfully",
	}, nil
}

func (s *Server) UnmountResource(ctx context.Context, req *sdspb.UnmountResourceRequest) (*sdspb.UnmountResourceResponse, error) {
	err := s.resources.Unmount(ctx, req.Resource, req.Node)
	if err != nil {
		return &sdspb.UnmountResourceResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.UnmountResourceResponse{
		Success: true,
		Message: "Resource unmounted successfully",
	}, nil
}

func (s *Server) MakeHa(ctx context.Context, req *sdspb.MakeHaRequest) (*sdspb.MakeHaResponse, error) {
	configPath, err := s.resources.MakeHa(ctx, req.Resource, req.Services, req.MountPoint, req.Fstype, req.Vip)
	if err != nil {
		return &sdspb.MakeHaResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.MakeHaResponse{
		Success: true,
		Message: "HA configuration created successfully",
		ConfigPath: configPath,
	}, nil
}

func (s *Server) EvictHa(ctx context.Context, req *sdspb.EvictHaRequest) (*sdspb.EvictHaResponse, error) {
	err := s.resources.EvictHa(ctx, req.Resource)
	if err != nil {
		return &sdspb.EvictHaResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.EvictHaResponse{
		Success: true,
		Message: "HA resource evicted successfully",
	}, nil
}

func (s *Server) DeleteHa(ctx context.Context, req *sdspb.DeleteHaRequest) (*sdspb.DeleteHaResponse, error) {
	err := s.resources.RemoveHa(ctx, req.Resource)
	if err != nil {
		return &sdspb.DeleteHaResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteHaResponse{
		Success: true,
		Message: "HA configuration deleted successfully",
	}, nil
}

func (s *Server) GetHa(ctx context.Context, req *sdspb.GetHaRequest) (*sdspb.GetHaResponse, error) {
	haCfg, err := s.resources.GetHaConfig(ctx, req.Resource)
	if err != nil {
		return &sdspb.GetHaResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	return &sdspb.GetHaResponse{
		Success: true,
		Message: "HA configuration found",
		Config: &sdspb.HaConfigInfo{
			Resource:   haCfg.Resource,
			Vip:        haCfg.VIP,
			MountPoint: haCfg.MountPoint,
			FsType:     haCfg.FsType,
			Services:   haCfg.Services,
		},
	}, nil
}

func (s *Server) ListHa(ctx context.Context, req *sdspb.ListHaRequest) (*sdspb.ListHaResponse, error) {
	haConfigs, err := s.resources.ListHaConfigs(ctx)
	if err != nil {
		return &sdspb.ListHaResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbConfigs []*sdspb.HaConfigInfo
	for _, cfg := range haConfigs {
		pbConfigs = append(pbConfigs, &sdspb.HaConfigInfo{
			Resource:   cfg.Resource,
			Vip:        cfg.VIP,
			MountPoint: cfg.MountPoint,
			FsType:     cfg.FsType,
			Services:   cfg.Services,
		})
	}

	return &sdspb.ListHaResponse{
		Success: true,
		Message: "HA configurations listed successfully",
		Configs: pbConfigs,
	}, nil
}

// ==================== SNAPSHOT OPERATIONS ====================

func (s *Server) CreateSnapshot(ctx context.Context, req *sdspb.CreateSnapshotRequest) (*sdspb.CreateSnapshotResponse, error) {
	err := s.snapshots.CreateSnapshot(ctx, req.Volume, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.CreateSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateSnapshotResponse{
		Success: true,
		Message: "Snapshot created successfully",
	}, nil
}

func (s *Server) DeleteSnapshot(ctx context.Context, req *sdspb.DeleteSnapshotRequest) (*sdspb.DeleteSnapshotResponse, error) {
	err := s.snapshots.DeleteSnapshot(ctx, req.Volume, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.DeleteSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteSnapshotResponse{
		Success: true,
		Message: "Snapshot deleted successfully",
	}, nil
}

func (s *Server) RestoreSnapshot(ctx context.Context, req *sdspb.RestoreSnapshotRequest) (*sdspb.RestoreSnapshotResponse, error) {
	err := s.snapshots.RestoreSnapshot(ctx, req.Volume, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.RestoreSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.RestoreSnapshotResponse{
		Success: true,
		Message: "Snapshot restored successfully",
	}, nil
}

func (s *Server) ListSnapshots(ctx context.Context, req *sdspb.ListSnapshotsRequest) (*sdspb.ListSnapshotsResponse, error) {
	snapshots, err := s.snapshots.ListSnapshots(ctx, req.Volume, req.Node)
	if err != nil {
		return &sdspb.ListSnapshotsResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbSnapshots []*sdspb.SnapshotInfo
	for _, snap := range snapshots {
		pbSnapshots = append(pbSnapshots, &sdspb.SnapshotInfo{
			Name:      snap.Name,
			Volume:    snap.Volume,
			SizeGb:    snap.SizeGB,
			CreatedAt: snap.CreatedAt,
		})
	}

	return &sdspb.ListSnapshotsResponse{
		Success: true,
		Message: "Snapshots listed successfully",
		Snapshots: pbSnapshots,
	}, nil
}

// ==================== GATEWAY OPERATIONS ====================

func (s *Server) CreateNFSGateway(ctx context.Context, req *sdspb.CreateNFSGatewayRequest) (*sdspb.CreateNFSGatewayResponse, error) {
	nfsMgr := gateway.NewNFSManager(s.gateway)
	resp, err := nfsMgr.CreateNFSGateway(ctx, req)
	if err != nil {
		return resp, err
	}

	// Generate gateway name from resource
	gwName := req.Resource + "-nfs"

	// Save to database
	if s.ctrl.db != nil {
		gw := &database.Gateway{
			Name:     gwName,
			Resource: req.Resource,
			Type:     database.GatewayTypeNFS,
			Config: map[string]interface{}{
				"service_ip":    req.ServiceIp,
				"export_path":   req.ExportPath,
				"allowed_ips":   req.AllowedIps,
				"fs_type":       req.FsType,
				"options":       req.Options,
			},
			Status: "created",
		}
		if err := s.ctrl.db.SaveGateway(ctx, gw); err != nil {
			s.ctrl.logger.Error("Failed to save gateway to database", zap.Error(err))
		}
	}

	return resp, nil
}

func (s *Server) CreateISCSIGateway(ctx context.Context, req *sdspb.CreateISCSIGatewayRequest) (*sdspb.CreateISCSIGatewayResponse, error) {
	iscsiMgr := gateway.NewISCSIManager(s.gateway)
	resp, err := iscsiMgr.CreateISCSIGateway(ctx, req)
	if err != nil {
		return resp, err
	}

	// Generate gateway name from resource
	gwName := req.Resource + "-iscsi"

	// Save to database
	if s.ctrl.db != nil {
		gw := &database.Gateway{
			Name:     gwName,
			Resource: req.Resource,
			Type:     database.GatewayTypeISCSI,
			Config: map[string]interface{}{
				"service_ip":         req.ServiceIp,
				"iqn":                req.Iqn,
				"allowed_initiators":  req.AllowedInitiators,
				"username":           req.Username,
				"password":           req.Password,
				"implementation":     req.Implementation,
				"options":            req.Options,
			},
			Status: "created",
		}
		if err := s.ctrl.db.SaveGateway(ctx, gw); err != nil {
			s.ctrl.logger.Error("Failed to save gateway to database", zap.Error(err))
		}
	}

	return resp, nil
}

func (s *Server) CreateNVMeGateway(ctx context.Context, req *sdspb.CreateNVMeGatewayRequest) (*sdspb.CreateNVMeGatewayResponse, error) {
	nvmeMgr := gateway.NewNVMeManager(s.gateway)
	resp, err := nvmeMgr.CreateNVMeGateway(ctx, req)
	if err != nil {
		return resp, err
	}

	// Generate gateway name from resource
	gwName := req.Resource + "-nvme"

	// Save to database
	if s.ctrl.db != nil {
		gw := &database.Gateway{
			Name:     gwName,
			Resource: req.Resource,
			Type:     database.GatewayTypeNVMEOF,
			Config: map[string]interface{}{
				"service_ip":      req.ServiceIp,
				"nqn":             req.Nqn,
				"transport_type":  req.TransportType,
				"options":         req.Options,
			},
			Status: "created",
		}
		if err := s.ctrl.db.SaveGateway(ctx, gw); err != nil {
			s.ctrl.logger.Error("Failed to save gateway to database", zap.Error(err))
		}
	}

	return resp, nil
}

func (s *Server) DeleteGateway(ctx context.Context, req *sdspb.DeleteGatewayRequest) (*sdspb.DeleteGatewayResponse, error) {
	err := s.gateway.DeleteGateway(ctx, req.Id)
	if err != nil {
		return &sdspb.DeleteGatewayResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	// Delete from database
	if s.ctrl.db != nil {
		if err := s.ctrl.db.DeleteGateway(ctx, req.Id); err != nil {
			s.ctrl.logger.Error("Failed to delete gateway from database", zap.Error(err))
		}
	}

	return &sdspb.DeleteGatewayResponse{
		Success: true,
		Message: "Gateway deleted successfully",
	}, nil
}

func (s *Server) GetGateway(ctx context.Context, req *sdspb.GetGatewayRequest) (*sdspb.GetGatewayResponse, error) {
	gw, err := s.gateway.GetGateway(ctx, req.Id)
	if err != nil {
		return &sdspb.GetGatewayResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.GetGatewayResponse{
		Success: true,
		Message: "Gateway found",
		Gateway: &sdspb.GatewayInfo{
			Id:       gw.ID,
			Name:     gw.Name,
			Type:     gw.Type,
			Resource: gw.Resource,
		},
	}, nil
}

func (s *Server) ListGateways(ctx context.Context, req *sdspb.ListGatewaysRequest) (*sdspb.ListGatewaysResponse, error) {
	gateways, err := s.gateway.ListGateways(ctx)
	if err != nil {
		return &sdspb.ListGatewaysResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbGateways []*sdspb.GatewayInfo
	for _, gw := range gateways {
		pbGateways = append(pbGateways, &sdspb.GatewayInfo{
			Id:       gw.ID,
			Name:     gw.Name,
			Type:     gw.Type,
			Resource: gw.Resource,
		})
	}

	return &sdspb.ListGatewaysResponse{
		Success: true,
		Message: "Gateways listed successfully",
		Gateways: pbGateways,
	}, nil
}

func (s *Server) StartGateway(ctx context.Context, req *sdspb.StartGatewayRequest) (*sdspb.StartGatewayResponse, error) {
	err := s.gateway.StartGateway(ctx, req.Id)
	if err != nil {
		return &sdspb.StartGatewayResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.StartGatewayResponse{
		Success: true,
		Message: "Gateway started successfully",
	}, nil
}

func (s *Server) StopGateway(ctx context.Context, req *sdspb.StopGatewayRequest) (*sdspb.StopGatewayResponse, error) {
	err := s.gateway.StopGateway(ctx, req.Id)
	if err != nil {
		return &sdspb.StopGatewayResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.StopGatewayResponse{
		Success: true,
		Message: "Gateway stopped successfully",
	}, nil
}
