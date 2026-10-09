package controller

import (
	"context"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/gateway"
)

// SMB gateway handlers. The gateway itself is in pkg/gateway/smb.go; stop,
// start and delete are the common gateway RPCs.

func (s *Server) CreateSMBGateway(ctx context.Context, req *haifypb.CreateSMBGatewayRequest) (*haifypb.CreateSMBGatewayResponse, error) {
	if err := s.ctrl.assertPromoterAllowed(ctx, req.Resource, "an SMB gateway"); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	resp, err := gateway.NewSMBManager(s.gateway).CreateSMBGateway(ctx, req)
	if err != nil {
		return resp, err
	}
	if s.ctrl.db != nil {
		gw := &database.Gateway{
			Name:     req.Resource + "-smb",
			Resource: req.Resource,
			Type:     database.GatewayTypeSMB,
			Config: map[string]interface{}{
				"service_ip":   req.ServiceIp,
				"service_host": gatewayServiceHost(req.ServiceIp),
				"workgroup":    req.Workgroup,
				"share_name":   req.ShareName,
			},
			Status: "configured",
		}
		if err := s.ctrl.db.SaveGateway(ctx, gw); err != nil {
			s.ctrl.logger.Error("Failed to save gateway to database", zap.Error(err))
		}
	}
	return resp, nil
}

func (s *Server) AddSMBShare(ctx context.Context, req *haifypb.AddSMBShareRequest) (*haifypb.AddSMBShareResponse, error) {
	sh := req.GetShare()
	if sh == nil {
		return nil, status.Error(codes.InvalidArgument, "share is required")
	}
	err := gateway.NewSMBManager(s.gateway).AddSMBShare(ctx, req.Resource, gateway.SMBShare{
		Name: sh.Name, Path: sh.Path, ReadOnly: sh.ReadOnly, ValidUsers: sh.ValidUsers,
	})
	if err != nil {
		return &haifypb.AddSMBShareResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.AddSMBShareResponse{Success: true, Message: "SMB share added"}, nil
}

func (s *Server) RemoveSMBShare(ctx context.Context, req *haifypb.RemoveSMBShareRequest) (*haifypb.RemoveSMBShareResponse, error) {
	if err := gateway.NewSMBManager(s.gateway).RemoveSMBShare(ctx, req.Resource, req.Name); err != nil {
		return &haifypb.RemoveSMBShareResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.RemoveSMBShareResponse{Success: true, Message: "SMB share removed; its data was left in place"}, nil
}

func (s *Server) ListSMBShares(ctx context.Context, req *haifypb.ListSMBSharesRequest) (*haifypb.ListSMBSharesResponse, error) {
	shares, err := gateway.NewSMBManager(s.gateway).ListSMBShares(ctx, req.Resource)
	if err != nil {
		return &haifypb.ListSMBSharesResponse{Success: false, Message: err.Error()}, nil
	}
	out := make([]*haifypb.SMBShareInfo, 0, len(shares))
	for _, sh := range shares {
		out = append(out, &haifypb.SMBShareInfo{Name: sh.Name, Path: sh.Path, ReadOnly: sh.ReadOnly, ValidUsers: sh.ValidUsers})
	}
	return &haifypb.ListSMBSharesResponse{Success: true, Shares: out}, nil
}

func (s *Server) SetSMBUser(ctx context.Context, req *haifypb.SetSMBUserRequest) (*haifypb.SetSMBUserResponse, error) {
	if err := gateway.NewSMBManager(s.gateway).SetSMBUser(ctx, req.Resource, req.User, req.Password); err != nil {
		return &haifypb.SetSMBUserResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.SetSMBUserResponse{Success: true, Message: "SMB user " + req.User + " set"}, nil
}

func (s *Server) RemoveSMBUser(ctx context.Context, req *haifypb.RemoveSMBUserRequest) (*haifypb.RemoveSMBUserResponse, error) {
	if err := gateway.NewSMBManager(s.gateway).RemoveSMBUser(ctx, req.Resource, req.User); err != nil {
		return &haifypb.RemoveSMBUserResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.RemoveSMBUserResponse{Success: true, Message: "SMB user " + req.User + " removed"}, nil
}

func (s *Server) ListSMBUsers(ctx context.Context, req *haifypb.ListSMBUsersRequest) (*haifypb.ListSMBUsersResponse, error) {
	users, err := gateway.NewSMBManager(s.gateway).ListSMBUsers(ctx, req.Resource)
	if err != nil {
		return &haifypb.ListSMBUsersResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.ListSMBUsersResponse{Success: true, Users: users}, nil
}
