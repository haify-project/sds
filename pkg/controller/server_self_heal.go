package controller

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// Self-healing RPCs (self_heal_*.go).

func (s *Server) MoveReplica(ctx context.Context, req *haifypb.MoveReplicaRequest) (*haifypb.MoveReplicaResponse, error) {
	if err := s.resources.MoveReplica(ctx, req.Resource, req.From, req.To); err != nil {
		return &haifypb.MoveReplicaResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.MoveReplicaResponse{Success: true, Message: fmt.Sprintf(
		"a replica of %s is being added on %s; the one on %s is removed once it is in sync", req.Resource, req.To, req.From)}, nil
}

func (s *Server) PlanRebalance(ctx context.Context, req *haifypb.PlanRebalanceRequest) (*haifypb.PlanRebalanceResponse, error) {
	moves, notes, err := s.resources.PlanRebalance(ctx, int(req.MaxMoves))
	if err != nil {
		return &haifypb.PlanRebalanceResponse{Success: false, Message: err.Error()}, nil
	}
	resp := &haifypb.PlanRebalanceResponse{Success: true, Notes: notes}
	for _, m := range moves {
		resp.Moves = append(resp.Moves, &haifypb.RebalanceMove{Resource: m.Resource, From: m.From, To: m.To, SizeGb: m.SizeGB})
	}
	switch {
	case len(moves) == 0:
		resp.Message = "the nodes are balanced; nothing to move"
	case req.Apply:
		if err := s.resources.ApplyRebalance(moves); err != nil {
			return &haifypb.PlanRebalanceResponse{Success: false, Message: err.Error(), Moves: resp.Moves}, nil
		}
		resp.Message = fmt.Sprintf("running %d move(s), one at a time", len(moves))
	default:
		resp.Message = fmt.Sprintf("%d move(s) proposed; nothing changed", len(moves))
	}
	return resp, nil
}

func (s *Server) MarkNodeLost(ctx context.Context, req *haifypb.MarkNodeLostRequest) (*haifypb.MarkNodeLostResponse, error) {
	done, err := s.resources.NodeLost(ctx, req.Node)
	if err != nil {
		return &haifypb.MarkNodeLostResponse{Success: false, Message: err.Error(), Resources: done}, nil
	}
	return &haifypb.MarkNodeLostResponse{Success: true, Resources: done,
		Message: fmt.Sprintf("%s is marked lost; %d replica(s) removed", req.Node, len(done))}, nil
}

func (s *Server) RestoreNode(ctx context.Context, req *haifypb.RestoreNodeRequest) (*haifypb.RestoreNodeResponse, error) {
	plan, err := s.resources.NodeRestore(ctx, req.Node, req.DryRun)
	if err != nil {
		return &haifypb.RestoreNodeResponse{Success: false, Message: err.Error(), Plan: plan}, nil
	}
	msg := fmt.Sprintf("%s is restored and may take replicas again", req.Node)
	if req.DryRun {
		msg = "dry run; nothing changed"
	}
	return &haifypb.RestoreNodeResponse{Success: true, Message: msg, Plan: plan}, nil
}

func (s *Server) SetHaPreferredNodes(ctx context.Context, req *haifypb.SetHaPreferredNodesRequest) (*haifypb.SetHaPreferredNodesResponse, error) {
	if err := s.resources.SetHaPreferredNodes(ctx, req.Resource, req.Nodes, req.Policy); err != nil {
		return &haifypb.SetHaPreferredNodesResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.SetHaPreferredNodesResponse{Success: true,
		Message: fmt.Sprintf("preferred nodes of %s set; the nodes that would take over use them now", req.Resource)}, nil
}
