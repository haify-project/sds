package controller

import (
	"context"
	"time"

	pb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/rbac"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The approval RPCs (approval.go). The authorization interceptor classifies
// ListApprovals as approval:read and the other two as approval:approve, which
// admin and security-officer hold. Each handler checks again, because a call
// that reaches the server without the interceptors (a test, an in-process
// client) must not decide a request on nobody's behalf.

func approvalInfo(a *database.Approval, now time.Time) *pb.ApprovalInfo {
	if a == nil {
		return nil
	}
	info := &pb.ApprovalInfo{
		Id: a.ID, Method: a.Method, Request: a.Request, Requester: a.Requester,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339), ExpiresAt: a.ExpiresAt.UTC().Format(time.RFC3339),
		State: a.EffectiveState(now), DecidedBy: a.DecidedBy,
	}
	if !a.DecidedAt.IsZero() {
		info.DecidedAt = a.DecidedAt.UTC().Format(time.RFC3339)
	}
	return info
}

// ListApprovals lists the requests, open ones only unless include_closed.
func (s *Server) ListApprovals(ctx context.Context, req *pb.ListApprovalsRequest) (*pb.ListApprovalsResponse, error) {
	g := s.ctrl.approvals
	if g == nil {
		return &pb.ListApprovalsResponse{Enabled: false}, nil
	}
	all, err := g.db.ListApprovals(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list approvals: %v", err)
	}
	now := g.now()
	resp := &pb.ListApprovalsResponse{Enabled: true}
	for _, a := range all {
		st := a.EffectiveState(now)
		if !req.GetIncludeClosed() && st != database.ApprovalPending && st != database.ApprovalApproved {
			continue
		}
		resp.Approvals = append(resp.Approvals, approvalInfo(a, now))
	}
	return resp, nil
}

// approver resolves a caller allowed to decide requests.
func (s *Server) approver(ctx context.Context) (string, error) {
	engine := s.ctrl.rbac
	if engine == nil {
		return "", status.Error(codes.FailedPrecondition, "two-person approval needs [rbac] enabled")
	}
	user, ok := rbacCaller(ctx, engine)
	if !ok {
		return "", errUnauthenticated
	}
	if allowed, _ := engine.Enforce(user, "approval", rbac.ActApprove); !allowed {
		return "", status.Error(codes.PermissionDenied, "approving needs the approve right (admin or security-officer)")
	}
	return user, nil
}

// ApproveRequest approves a pending request made by someone else.
func (s *Server) ApproveRequest(ctx context.Context, req *pb.ApproveRequestRequest) (*pb.ApproveRequestResponse, error) {
	user, err := s.approver(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.ctrl.approvals.decide(ctx, req.GetId(), user, true)
	if err != nil {
		return &pb.ApproveRequestResponse{Success: false, Message: err.Error(), Approval: approvalInfo(a, time.Now())}, nil
	}
	return &pb.ApproveRequestResponse{Success: true, Approval: approvalInfo(a, time.Now()),
		Message: a.Requester + " may now repeat the call within " + s.ctrl.approvals.ttl.String()}, nil
}

// RejectRequest closes a pending request without running it.
func (s *Server) RejectRequest(ctx context.Context, req *pb.RejectRequestRequest) (*pb.RejectRequestResponse, error) {
	user, err := s.approver(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.ctrl.approvals.decide(ctx, req.GetId(), user, false)
	if err != nil {
		return &pb.RejectRequestResponse{Success: false, Message: err.Error(), Approval: approvalInfo(a, time.Now())}, nil
	}
	return &pb.RejectRequestResponse{Success: true, Message: "rejected", Approval: approvalInfo(a, time.Now())}, nil
}
