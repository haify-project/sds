package controller

import (
	"context"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/gateway"
	"go.uber.org/zap"
)

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
