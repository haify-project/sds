package controller

import (
	"context"
	"strings"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
	// Ordered start[] list: systemd units and OCF agents interleaved as peers.
	var startItems []HaStartItem
	for _, it := range req.StartItems {
		if it == nil {
			continue
		}
		if ocf := it.GetOcf(); ocf != nil {
			startItems = append(startItems, HaStartItem{Ocf: &OcfAgentSpec{
				Provider: ocf.Provider,
				Name:     ocf.Name,
				Instance: ocf.Instance,
				Params:   ocf.Params,
			}})
		} else if unit := strings.TrimSpace(it.GetSystemdUnit()); unit != "" {
			startItems = append(startItems, HaStartItem{SystemdUnit: unit})
		}
	}
	configPath, err := s.resources.MakeHa(ctx, req.Resource, req.Services, req.MountPoint, req.Fstype, req.Vip, ocfAgents, startItems)
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

func (s *Server) GetHaStatus(ctx context.Context, req *sdspb.GetHaStatusRequest) (*sdspb.GetHaStatusResponse, error) {
	promoters, err := s.resources.GetHaStatus(ctx, req.Resource)
	if err != nil {
		return &sdspb.GetHaStatusResponse{Success: false, Message: err.Error()}, nil
	}

	var pbPromoters []*sdspb.HaPromoterStatus
	for _, p := range promoters {
		pb := &sdspb.HaPromoterStatus{
			DrbdResource: p.DRBDResource,
			PrimaryOn:    p.PrimaryOn,
			Status:       p.Status,
			Target:       &sdspb.HaServiceStatus{Name: p.Target.Name, Status: p.Target.Status},
		}
		for _, d := range p.Deps {
			pb.Deps = append(pb.Deps, &sdspb.HaServiceStatus{Name: d.Name, Status: d.Status})
		}
		pbPromoters = append(pbPromoters, pb)
	}

	return &sdspb.GetHaStatusResponse{
		Success:   true,
		Message:   "HA status retrieved successfully",
		Promoters: pbPromoters,
	}, nil
}
