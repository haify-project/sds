package controller

import (
	"context"
	"fmt"
	"time"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/gateway"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func (s *Server) HealthCheck(ctx context.Context, req *sdspb.HealthCheckRequest) (*sdspb.HealthCheckResponse, error) {
	health, err := s.nodes.HealthCheck(ctx, req.Node)
	if err != nil {
		return &sdspb.HealthCheckResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	return &sdspb.HealthCheckResponse{
		Success: true,
		Message: "Health check completed",
		Health: &sdspb.NodeHealthInfo{
			DrbdInstalled:           health.DrbdInstalled,
			DrbdVersion:             health.DrbdVersion,
			DrbdReactorInstalled:    health.DrbdReactorInstalled,
			DrbdReactorVersion:      health.DrbdReactorVersion,
			DrbdReactorRunning:      health.DrbdReactorRunning,
			ResourceAgentsInstalled: health.ResourceAgentsInstalled,
			AvailableAgents:         health.AvailableAgents,
		},
	}, nil
}

// ==================== RESOURCE OPERATIONS ====================

func (s *Server) CreateResource(ctx context.Context, req *sdspb.CreateResourceRequest) (*sdspb.CreateResourceResponse, error) {
	// Prefer the explicit multi-volume list; fall back to the single-volume
	// size_gb/pool shorthand when it is empty (older clients, CLI).
	volumes := make([]VolumeSpec, 0, len(req.Volumes))
	for _, v := range req.Volumes {
		volumes = append(volumes, VolumeSpec{SizeGB: v.SizeGb, Pool: v.Pool})
	}
	if len(volumes) == 0 {
		volumes = append(volumes, VolumeSpec{SizeGB: req.SizeGb, Pool: req.Pool})
	}
	err := s.resources.CreateResourceWithVolumes(ctx, req.Name, req.Port, req.Nodes, req.Protocol, req.StorageType, req.DrbdOptions, volumes)
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

func (s *Server) AdoptResource(ctx context.Context, req *sdspb.AdoptResourceRequest) (*sdspb.AdoptResourceResponse, error) {
	result, err := s.resources.AdoptResource(ctx, req.Name, req.Nodes, req.Port, req.Protocol)
	if err != nil {
		return &sdspb.AdoptResourceResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.AdoptResourceResponse{
		Success:  true,
		Message:  "Resource adopted successfully",
		Nodes:    result.Nodes,
		Port:     result.Port,
		Protocol: result.Protocol,
		Volumes:  uint32(result.Volumes),
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
			VolumeId:      v.VolumeID,
			Device:        v.Device,
			SizeGb:        v.SizeGB,
			Pool:          v.Pool,
			BackingVolume: v.BackingVolume,
		})
	}

	// Build node states map
	nodeStates := make(map[string]*sdspb.NodeResourceState)
	for node, state := range resource.NodeStates {
		nodeStates[node] = &sdspb.NodeResourceState{
			Role:             state.Role,
			DiskState:        state.DiskState,
			ReplicationState: state.Replication,
			SyncPercent:      state.SyncPercent,
		}
	}

	return &sdspb.GetResourceResponse{
		Success: true,
		Message: "Resource found",
		Resource: &sdspb.ResourceInfo{
			Name:          resource.Name,
			Port:          resource.Port,
			Protocol:      resource.Protocol,
			Nodes:         resource.Nodes,
			Role:          resource.Role,
			Volumes:       pbVolumes,
			NodeStates:    nodeStates,
			DisklessNodes: resource.DisklessNodes,
			QuorumRisk:    resource.QuorumRisk,
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
				VolumeId:      v.VolumeID,
				Device:        v.Device,
				SizeGb:        v.SizeGB,
				Pool:          v.Pool,
				BackingVolume: v.BackingVolume,
			})
		}
		pbResources = append(pbResources, &sdspb.ResourceInfo{
			Name:          r.Name,
			Port:          r.Port,
			Protocol:      r.Protocol,
			Nodes:         r.Nodes,
			Role:          r.Role,
			Volumes:       pbVolumes,
			DisklessNodes: r.DisklessNodes,
			QuorumRisk:    r.QuorumRisk,
		})
	}

	return &sdspb.ListResourcesResponse{
		Success:   true,
		Message:   "Resources listed successfully",
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

func (s *Server) UpdateResourceOptions(ctx context.Context, req *sdspb.UpdateResourceOptionsRequest) (*sdspb.UpdateResourceOptionsResponse, error) {
	if err := s.resources.SetOptions(ctx, req.Name, req.Options); err != nil {
		return &sdspb.UpdateResourceOptionsResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.UpdateResourceOptionsResponse{
		Success: true,
		Message: "Resource options updated and applied",
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
		Name:       resource.Name,
		Role:       resource.Role,
		Nodes:      resource.Nodes,
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
			SyncPercent:      nodeState.SyncPercent,
		}
	}

	for _, v := range resource.Volumes {
		status.Volumes = append(status.Volumes, &sdspb.VolumeInfo{
			VolumeId:      v.VolumeID,
			Device:        v.Device,
			SizeGb:        v.SizeGB,
			Pool:          v.Pool,
			BackingVolume: v.BackingVolume,
		})
	}

	return &sdspb.ResourceStatusResponse{
		Success: true,
		Message: "Resource status retrieved",
		Status:  status,
	}, nil
}

func (s *Server) SetPrimary(ctx context.Context, req *sdspb.SetPrimaryRequest) (*sdspb.SetPrimaryResponse, error) {
	var err error
	if req.QuorumGuarded {
		// Quorum-guarded promote: try normal, escalate to --force only if the
		// node holds DRBD quorum, refuse otherwise. `force` is ignored here.
		err = s.resources.PromoteForNode(ctx, req.Resource, req.Node)
	} else {
		err = s.resources.SetPrimary(ctx, req.Resource, req.Node, req.Force)
	}
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
	err := s.resources.CreateFilesystemOnly(ctx, req.Resource, req.VolumeId, req.Fstype, req.Node)
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
	err := s.resources.Mount(ctx, req.Resource, req.Path, req.VolumeId, req.Node, req.Fstype)
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
	err := s.resources.Unmount(ctx, req.Resource, req.VolumeId, req.Node)
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
	var ocfAgents []OcfAgentSpec
	for _, a := range req.OcfAgents {
		if a == nil {
			continue
		}
		ocfAgents = append(ocfAgents, OcfAgentSpec{
			Provider: a.Provider,
			Name:     a.Name,
			Instance: a.Instance,
			Params:   a.Params,
		})
	}
	configPath, err := s.resources.MakeHa(ctx, req.Resource, req.Services, req.MountPoint, req.Fstype, req.Vip, ocfAgents)
	if err != nil {
		return &sdspb.MakeHaResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.MakeHaResponse{
		Success:    true,
		Message:    "HA configuration created successfully",
		ConfigPath: configPath,
	}, nil
}

// ListResourceAgents lists the OCF resource agents available on the nodes.
func (s *Server) ListResourceAgents(ctx context.Context, req *sdspb.ListResourceAgentsRequest) (*sdspb.ListResourceAgentsResponse, error) {
	agents, err := s.resources.ListResourceAgents(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	resp := &sdspb.ListResourceAgentsResponse{}
	for _, a := range agents {
		resp.Agents = append(resp.Agents, &sdspb.ResourceAgentInfo{
			Provider:  a.Provider,
			Name:      a.Name,
			Shortdesc: a.Shortdesc,
		})
	}
	return resp, nil
}

// GetResourceAgentMetadata returns an OCF agent's parsed meta-data parameter schema.
func (s *Server) GetResourceAgentMetadata(ctx context.Context, req *sdspb.GetResourceAgentMetadataRequest) (*sdspb.GetResourceAgentMetadataResponse, error) {
	meta, err := s.resources.GetResourceAgentMetadata(ctx, req.Provider, req.Name)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	resp := &sdspb.GetResourceAgentMetadataResponse{
		Provider:  meta.Provider,
		Name:      meta.Name,
		Version:   meta.Version,
		Shortdesc: meta.Shortdesc,
		Longdesc:  meta.Longdesc,
	}
	for _, p := range meta.Parameters {
		resp.Parameters = append(resp.Parameters, &sdspb.ResourceAgentParameter{
			Name:      p.Name,
			Required:  p.Required,
			Unique:    p.Unique,
			Type:      p.Type,
			Default:   p.Default,
			Shortdesc: p.Shortdesc,
			Longdesc:  p.Longdesc,
		})
	}
	return resp, nil
}

// GetHaToml reads a resource's drbd-reactor promoter TOML.
func (s *Server) GetHaToml(ctx context.Context, req *sdspb.GetHaTomlRequest) (*sdspb.GetHaTomlResponse, error) {
	path, content, err := s.resources.GetHaToml(ctx, req.Resource)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &sdspb.GetHaTomlResponse{
		Resource: req.Resource,
		Path:     path,
		Content:  content,
	}, nil
}

// SyncHaToml writes an edited promoter TOML to all resource nodes and reloads drbd-reactor.
func (s *Server) SyncHaToml(ctx context.Context, req *sdspb.SyncHaTomlRequest) (*sdspb.SyncHaTomlResponse, error) {
	message, err := s.resources.SyncHaToml(ctx, req.Resource, req.Content)
	if err != nil {
		return &sdspb.SyncHaTomlResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.SyncHaTomlResponse{
		Success: true,
		Message: message,
	}, nil
}

func (s *Server) EnableSelfHa(ctx context.Context, req *sdspb.EnableSelfHaRequest) (*sdspb.EnableSelfHaResponse, error) {
	handoffLog, err := s.resources.EnableSelfHa(ctx, req.Vip, req.Pool, req.SizeGb, req.Port, req.Nodes)
	if err != nil {
		return &sdspb.EnableSelfHaResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.EnableSelfHaResponse{
		Success:    true,
		Message:    "Self-HA handoff started; the controller will restart under drbd-reactor management",
		Resource:   SelfHaResource,
		HandoffLog: handoffLog,
	}, nil
}

func (s *Server) DisableSelfHa(ctx context.Context, req *sdspb.DisableSelfHaRequest) (*sdspb.DisableSelfHaResponse, error) {
	if err := s.resources.DisableSelfHa(ctx, req.Node); err != nil {
		return &sdspb.DisableSelfHaResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DisableSelfHaResponse{
		Success: true,
		Message: "Self-HA disable started; the controller will restart standalone on " + req.Node,
	}, nil
}

func (s *Server) GetSelfHaStatus(ctx context.Context, req *sdspb.GetSelfHaStatusRequest) (*sdspb.GetSelfHaStatusResponse, error) {
	status, err := s.resources.GetSelfHaStatus(ctx)
	if err != nil {
		return &sdspb.GetSelfHaStatusResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.GetSelfHaStatusResponse{
		Success:    true,
		Message:    "OK",
		Enabled:    status.Enabled,
		Resource:   status.Resource,
		Vip:        status.VIP,
		Nodes:      status.Nodes,
		ActiveNode: status.ActiveNode,
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
		Success:   true,
		Message:   "Snapshots listed successfully",
		Snapshots: pbSnapshots,
	}, nil
}

// ==================== SNAPSHOT SCHEDULE OPERATIONS ====================

func (s *Server) CreateSnapshotSchedule(ctx context.Context, req *sdspb.CreateSnapshotScheduleRequest) (*sdspb.CreateSnapshotScheduleResponse, error) {
	err := s.ctrl.schedules.CreateSchedule(ctx, req.Resource, req.Cron, gfsFromProto(req.Keep), req.Enabled)
	if err != nil {
		return &sdspb.CreateSnapshotScheduleResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.CreateSnapshotScheduleResponse{
		Success: true,
		Message: fmt.Sprintf("Snapshot schedule for %q created", req.Resource),
	}, nil
}

func (s *Server) ListSnapshotSchedules(ctx context.Context, req *sdspb.ListSnapshotSchedulesRequest) (*sdspb.ListSnapshotSchedulesResponse, error) {
	schedules, err := s.ctrl.schedules.ListSchedules(ctx)
	if err != nil {
		return &sdspb.ListSnapshotSchedulesResponse{Success: false, Message: err.Error()}, nil
	}
	now := time.Now()
	var out []*sdspb.SnapshotScheduleInfo
	for _, sc := range schedules {
		info := &sdspb.SnapshotScheduleInfo{
			Name:     sc.Name,
			Resource: sc.Resource,
			Cron:     sc.Cron,
			Enabled:  sc.Enabled,
			Keep:     gfsToProto(sc.Keep),
		}
		if !sc.LastRun.IsZero() {
			info.LastRun = sc.LastRun.UTC().Format(time.RFC3339)
		}
		if sc.Enabled {
			if next := NextRun(sc.Cron, now); !next.IsZero() {
				info.NextRun = next.UTC().Format(time.RFC3339)
			}
		}
		out = append(out, info)
	}
	return &sdspb.ListSnapshotSchedulesResponse{
		Success:   true,
		Message:   "Snapshot schedules listed successfully",
		Schedules: out,
	}, nil
}

func (s *Server) DeleteSnapshotSchedule(ctx context.Context, req *sdspb.DeleteSnapshotScheduleRequest) (*sdspb.DeleteSnapshotScheduleResponse, error) {
	if err := s.ctrl.schedules.DeleteSchedule(ctx, req.Name); err != nil {
		return &sdspb.DeleteSnapshotScheduleResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.DeleteSnapshotScheduleResponse{
		Success: true,
		Message: fmt.Sprintf("Snapshot schedule %q deleted", req.Name),
	}, nil
}

func gfsFromProto(p *sdspb.GFSRetention) database.GFSPolicy {
	if p == nil {
		return database.GFSPolicy{}
	}
	return database.GFSPolicy{
		Hourly:  int(p.Hourly),
		Daily:   int(p.Daily),
		Weekly:  int(p.Weekly),
		Monthly: int(p.Monthly),
		Yearly:  int(p.Yearly),
	}
}

func gfsToProto(p database.GFSPolicy) *sdspb.GFSRetention {
	return &sdspb.GFSRetention{
		Hourly:  int32(p.Hourly),
		Daily:   int32(p.Daily),
		Weekly:  int32(p.Weekly),
		Monthly: int32(p.Monthly),
		Yearly:  int32(p.Yearly),
	}
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
				"service_ip":       req.ServiceIp,
				"service_host":     gatewayServiceHost(req.ServiceIp),
				"export_path":      req.ExportPath,
				"export_directory": gatewayExportDirectory(req.Resource, req.ExportPath),
				"allowed_ips":      req.AllowedIps,
				"fs_type":          req.FsType,
				"options":          req.Options,
			},
			Status: "configured",
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
				"service_host":       gatewayServiceHost(req.ServiceIp),
				"iqn":                req.Iqn,
				"allowed_initiators": req.AllowedInitiators,
				"username":           req.Username,
				"password":           req.Password,
				"implementation":     req.Implementation,
				"options":            req.Options,
			},
			Status: "configured",
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
				"service_ip":     req.ServiceIp,
				"service_host":   gatewayServiceHost(req.ServiceIp),
				"nqn":            req.Nqn,
				"transport_type": req.TransportType,
				"options":        req.Options,
			},
			Status: "configured",
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
		if err := s.ctrl.db.DeleteGatewayByResource(ctx, req.Id); err != nil {
			s.ctrl.logger.Error("Failed to delete gateway from database", zap.Error(err))
		}
	}

	return &sdspb.DeleteGatewayResponse{
		Success: true,
		Message: "Gateway deleted successfully",
	}, nil
}

func (s *Server) GetGateway(ctx context.Context, req *sdspb.GetGatewayRequest) (*sdspb.GetGatewayResponse, error) {
	gw, err := s.getGatewayInfo(ctx, req.Id)
	if err != nil {
		return &sdspb.GetGatewayResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.GetGatewayResponse{
		Success: true,
		Message: "Gateway found",
		Gateway: s.enrichGatewayInfo(ctx, gw),
	}, nil
}

func (s *Server) ListGateways(ctx context.Context, req *sdspb.ListGatewaysRequest) (*sdspb.ListGatewaysResponse, error) {
	gateways, err := s.listGatewayInfos(ctx)
	if err != nil {
		return &sdspb.ListGatewaysResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbGateways []*sdspb.GatewayInfo
	for _, gw := range gateways {
		pbGateways = append(pbGateways, s.enrichGatewayInfo(ctx, gw))
	}

	return &sdspb.ListGatewaysResponse{
		Success:  true,
		Message:  "Gateways listed successfully",
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
	if s.ctrl.db != nil {
		if gw, err := s.ctrl.db.GetGatewayByResource(ctx, req.Id); err == nil {
			gw.Status = "started"
			if err := s.ctrl.db.SaveGateway(ctx, gw); err != nil {
				s.ctrl.logger.Error("Failed to update gateway status in database", zap.Error(err))
			}
		}
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
	if s.ctrl.db != nil {
		if gw, err := s.ctrl.db.GetGatewayByResource(ctx, req.Id); err == nil {
			gw.Status = "stopped"
			gw.ActiveNode = ""
			if err := s.ctrl.db.SaveGateway(ctx, gw); err != nil {
				s.ctrl.logger.Error("Failed to update gateway status in database", zap.Error(err))
			}
		}
	}
	return &sdspb.StopGatewayResponse{
		Success: true,
		Message: "Gateway stopped successfully",
	}, nil
}

// ==================== ZFS POOL OPERATIONS ====================

func (s *Server) CreateZFSPool(ctx context.Context, req *sdspb.CreateZFSPoolRequest) (*sdspb.CreateZFSPoolResponse, error) {
	err := s.storage.CreateZFSPool(ctx, req.Name, req.Node, req.Vdevs)
	if err != nil {
		return &sdspb.CreateZFSPoolResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateZFSPoolResponse{
		Success: true,
		Message: "ZFS pool created successfully",
	}, nil
}

func (s *Server) DeleteZFSPool(ctx context.Context, req *sdspb.DeleteZFSPoolRequest) (*sdspb.DeleteZFSPoolResponse, error) {
	err := s.storage.DeleteZFSPool(ctx, req.Name, req.Node)
	if err != nil {
		return &sdspb.DeleteZFSPoolResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteZFSPoolResponse{
		Success: true,
		Message: "ZFS pool deleted successfully",
	}, nil
}

func (s *Server) ListZFSpools(ctx context.Context, req *sdspb.ListZFSPoolsRequest) (*sdspb.ListZFSPoolsResponse, error) {
	pools, err := s.storage.ListZFSpools(ctx)
	if err != nil {
		return &sdspb.ListZFSPoolsResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	var pbPools []*sdspb.PoolInfo
	for _, p := range pools {
		pbPools = append(pbPools, &sdspb.PoolInfo{
			Name:        p.Name,
			Type:        p.Type,
			Node:        p.Node,
			TotalGb:     p.TotalGB,
			FreeGb:      p.FreeGB,
			Devices:     p.Devices,
			Thin:        p.Thin,
			Compression: p.Compression,
		})
	}

	return &sdspb.ListZFSPoolsResponse{
		Success: true,
		Message: "ZFS pools listed successfully",
		Pools:   pbPools,
	}, nil
}

func (s *Server) CreateZFSDataset(ctx context.Context, req *sdspb.CreateZFSDatasetRequest) (*sdspb.CreateZFSDatasetResponse, error) {
	err := s.storage.CreateZFSDataset(ctx, req.DatasetPath, req.Node)
	if err != nil {
		return &sdspb.CreateZFSDatasetResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateZFSDatasetResponse{
		Success: true,
		Message: "ZFS dataset created successfully",
	}, nil
}

func (s *Server) CreateZFSVolume(ctx context.Context, req *sdspb.CreateZFSVolumeRequest) (*sdspb.CreateZFSVolumeResponse, error) {
	err := s.storage.CreateZFSThinVolume(ctx, req.PoolName, req.VolumeName, req.Size, req.Node)
	if err != nil {
		return &sdspb.CreateZFSVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateZFSVolumeResponse{
		Success: true,
		Message: "ZFS volume created successfully",
	}, nil
}

func (s *Server) ResizeZFSVolume(ctx context.Context, req *sdspb.ResizeZFSVolumeRequest) (*sdspb.ResizeZFSVolumeResponse, error) {
	err := s.storage.ZFSResizeVolume(ctx, req.VolumePath, req.NewSize, req.Node)
	if err != nil {
		return &sdspb.ResizeZFSVolumeResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.ResizeZFSVolumeResponse{
		Success: true,
		Message: "ZFS volume resized successfully",
	}, nil
}

func (s *Server) DeleteZFSDataset(ctx context.Context, req *sdspb.DeleteZFSDatasetRequest) (*sdspb.DeleteZFSDatasetResponse, error) {
	// Use ZFS destroy for both datasets and volumes
	err := s.storage.ZFSDeleteDataset(ctx, req.DatasetPath, req.Node)
	if err != nil {
		return &sdspb.DeleteZFSDatasetResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteZFSDatasetResponse{
		Success: true,
		Message: "ZFS dataset deleted successfully",
	}, nil
}

// ==================== ZFS SNAPSHOT OPERATIONS ====================

func (s *Server) CreateZFSSnapshot(ctx context.Context, req *sdspb.CreateZFSSnapshotRequest) (*sdspb.CreateZFSSnapshotResponse, error) {
	err := s.storage.ZFSSnapshot(ctx, req.Dataset, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.CreateZFSSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateZFSSnapshotResponse{
		Success: true,
		Message: "ZFS snapshot created successfully",
	}, nil
}

func (s *Server) DeleteZFSSnapshot(ctx context.Context, req *sdspb.DeleteZFSSnapshotRequest) (*sdspb.DeleteZFSSnapshotResponse, error) {
	err := s.storage.ZFSDeleteSnapshot(ctx, req.Snapshot, req.Node)
	if err != nil {
		return &sdspb.DeleteZFSSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteZFSSnapshotResponse{
		Success: true,
		Message: "ZFS snapshot deleted successfully",
	}, nil
}

func (s *Server) ListZFSSnapshots(ctx context.Context, req *sdspb.ListZFSSnapshotsRequest) (*sdspb.ListZFSSnapshotsResponse, error) {
	snapshots, err := s.storage.ZFSListSnapshots(ctx, req.Dataset, req.Node)
	if err != nil {
		return &sdspb.ListZFSSnapshotsResponse{
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

	return &sdspb.ListZFSSnapshotsResponse{
		Success:   true,
		Message:   "ZFS snapshots listed successfully",
		Snapshots: pbSnapshots,
	}, nil
}

func (s *Server) RestoreZFSSnapshot(ctx context.Context, req *sdspb.RestoreZFSSnapshotRequest) (*sdspb.RestoreZFSSnapshotResponse, error) {
	err := s.storage.ZFSRestoreSnapshot(ctx, req.Dataset, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.RestoreZFSSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.RestoreZFSSnapshotResponse{
		Success: true,
		Message: "ZFS snapshot restored successfully",
	}, nil
}

func (s *Server) CloneZFSSnapshot(ctx context.Context, req *sdspb.CloneZFSSnapshotRequest) (*sdspb.CloneZFSSnapshotResponse, error) {
	err := s.storage.ZFSCloneSnapshot(ctx, req.Snapshot, req.ClonePath, req.Node)
	if err != nil {
		return &sdspb.CloneZFSSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CloneZFSSnapshotResponse{
		Success: true,
		Message: "ZFS snapshot cloned successfully",
	}, nil
}

// ==================== LVM SNAPSHOT OPERATIONS ====================

func (s *Server) CreateLvmSnapshot(ctx context.Context, req *sdspb.CreateLvmSnapshotRequest) (*sdspb.CreateLvmSnapshotResponse, error) {
	err := s.storage.CreateLvmSnapshot(ctx, req.Resource, req.LvName, req.SnapshotName, req.Node, req.Size)
	if err != nil {
		return &sdspb.CreateLvmSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.CreateLvmSnapshotResponse{
		Success: true,
		Message: "LVM snapshot created successfully",
	}, nil
}

func (s *Server) DeleteLvmSnapshot(ctx context.Context, req *sdspb.DeleteLvmSnapshotRequest) (*sdspb.DeleteLvmSnapshotResponse, error) {
	err := s.storage.DeleteLvmSnapshot(ctx, req.LvName, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.DeleteLvmSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.DeleteLvmSnapshotResponse{
		Success: true,
		Message: "LVM snapshot deleted successfully",
	}, nil
}

func (s *Server) ListLvmSnapshots(ctx context.Context, req *sdspb.ListLvmSnapshotsRequest) (*sdspb.ListLvmSnapshotsResponse, error) {
	snapshots, err := s.storage.ListLvmSnapshots(ctx, req.LvName, req.Node)
	if err != nil {
		return &sdspb.ListLvmSnapshotsResponse{
			Success:   false,
			Message:   err.Error(),
			Snapshots: nil,
		}, nil
	}
	// Convert to proto SnapshotInfo
	var protoSnapshots []*sdspb.SnapshotInfo
	for _, snap := range snapshots {
		protoSnapshots = append(protoSnapshots, &sdspb.SnapshotInfo{
			Name:      snap.Name,
			Volume:    snap.Volume,
			SizeGb:    snap.SizeGB,
			CreatedAt: snap.CreatedAt,
		})
	}
	return &sdspb.ListLvmSnapshotsResponse{
		Success:   true,
		Message:   "LVM snapshots listed successfully",
		Snapshots: protoSnapshots,
	}, nil
}

func (s *Server) RestoreLvmSnapshot(ctx context.Context, req *sdspb.RestoreLvmSnapshotRequest) (*sdspb.RestoreLvmSnapshotResponse, error) {
	err := s.storage.RestoreLvmSnapshot(ctx, req.LvName, req.SnapshotName, req.Node)
	if err != nil {
		return &sdspb.RestoreLvmSnapshotResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &sdspb.RestoreLvmSnapshotResponse{
		Success: true,
		Message: "LVM snapshot restored successfully",
	}, nil
}
