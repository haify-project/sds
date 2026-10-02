package controller

import (
	"context"
	"strings"

	pb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/rbac"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The RBAC RPCs mirror the REST routes in rbac_routes.go. They exist so the
// CLI reaches them over the gRPC listener, which is the one [tls] protects:
// a user-management call carries an admin's bearer token and, for `user add`,
// returns a new one.
//
// The authorization interceptor classifies these as the "system" object, so
// by default only admin may call the mutating ones. Each handler still checks
// CanAdmin itself, because a custom policy granting system:read (every viewer
// has it) must not expose the user list, and one granting system:write must
// not hand out user management.

var errAdminRequired = status.Error(codes.PermissionDenied, "administrator role required")

// rbacCaller resolves the calling user. The identity interceptor has normally
// done it already; the token is the fallback for a call that skipped it.
func rbacCaller(ctx context.Context, engine *rbac.Engine) (string, bool) {
	if name := userFromContext(ctx); name != "" {
		return name, true
	}
	return engine.ResolveUser(bearerToken(ctx))
}

// rbacRequireAdmin returns nil when the caller holds the administrator role.
func rbacRequireAdmin(ctx context.Context, engine *rbac.Engine) error {
	user, ok := rbacCaller(ctx, engine)
	if !ok {
		return errUnauthenticated
	}
	if !engine.CanAdmin(user) {
		return errAdminRequired
	}
	return nil
}

// GetRbacWhoami reports the caller's identity and role.
func (s *Server) GetRbacWhoami(ctx context.Context, _ *pb.GetRbacWhoamiRequest) (*pb.GetRbacWhoamiResponse, error) {
	engine := s.ctrl.rbac
	if engine == nil {
		return &pb.GetRbacWhoamiResponse{Enabled: false}, nil
	}
	user, ok := rbacCaller(ctx, engine)
	if !ok {
		return nil, errUnauthenticated
	}
	return &pb.GetRbacWhoamiResponse{
		Enabled:  true,
		User:     user,
		Role:     engine.RoleOf(user),
		CanAdmin: engine.CanAdmin(user),
	}, nil
}

// ListRbacPolicies returns the effective policy and user assignments. Admin only.
func (s *Server) ListRbacPolicies(ctx context.Context, _ *pb.ListRbacPoliciesRequest) (*pb.ListRbacPoliciesResponse, error) {
	engine := s.ctrl.rbac
	if engine == nil {
		return &pb.ListRbacPoliciesResponse{Enabled: false}, nil
	}
	if err := rbacRequireAdmin(ctx, engine); err != nil {
		return nil, err
	}
	resp := &pb.ListRbacPoliciesResponse{Enabled: true}
	for _, p := range engine.EffectivePolicies() {
		resp.Policies = append(resp.Policies, &pb.RbacPolicy{Role: p.Role, Object: p.Object, Action: p.Action})
	}
	for _, u := range engine.Users() {
		resp.Users = append(resp.Users, &pb.RbacUser{Name: u.Name, Role: u.Role})
	}
	return resp, nil
}

// CreateRbacUser adds a user and returns its token, the only time it is shown.
func (s *Server) CreateRbacUser(ctx context.Context, req *pb.CreateRbacUserRequest) (*pb.CreateRbacUserResponse, error) {
	engine := s.ctrl.rbac
	if engine == nil {
		return &pb.CreateRbacUserResponse{Message: "RBAC is not enabled"}, nil
	}
	if err := rbacRequireAdmin(ctx, engine); err != nil {
		return nil, err
	}
	token, err := engine.CreateUser(strings.TrimSpace(req.GetName()), strings.TrimSpace(req.GetToken()), req.GetRole())
	if err != nil {
		return &pb.CreateRbacUserResponse{Enabled: true, Message: err.Error()}, nil
	}
	return &pb.CreateRbacUserResponse{Success: true, Enabled: true, Token: token}, nil
}

// DeleteRbacUser removes a runtime-managed user. Admin only.
func (s *Server) DeleteRbacUser(ctx context.Context, req *pb.DeleteRbacUserRequest) (*pb.DeleteRbacUserResponse, error) {
	engine := s.ctrl.rbac
	if engine == nil {
		return &pb.DeleteRbacUserResponse{Message: "RBAC is not enabled"}, nil
	}
	if err := rbacRequireAdmin(ctx, engine); err != nil {
		return nil, err
	}
	if err := engine.DeleteUser(req.GetName()); err != nil {
		return &pb.DeleteRbacUserResponse{Enabled: true, Message: err.Error()}, nil
	}
	return &pb.DeleteRbacUserResponse{Success: true, Enabled: true}, nil
}

// SetRbacUserRole changes a user's role. Admin only.
func (s *Server) SetRbacUserRole(ctx context.Context, req *pb.SetRbacUserRoleRequest) (*pb.SetRbacUserRoleResponse, error) {
	engine := s.ctrl.rbac
	if engine == nil {
		return &pb.SetRbacUserRoleResponse{Message: "RBAC is not enabled"}, nil
	}
	if err := rbacRequireAdmin(ctx, engine); err != nil {
		return nil, err
	}
	if err := engine.SetRole(req.GetName(), req.GetRole()); err != nil {
		return &pb.SetRbacUserRoleResponse{Enabled: true, Message: err.Error()}, nil
	}
	return &pb.SetRbacUserRoleResponse{Success: true, Enabled: true}, nil
}
