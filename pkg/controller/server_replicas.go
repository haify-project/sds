package controller

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

func (s *Server) AttachDisklessClient(ctx context.Context, req *haifypb.AttachDisklessClientRequest) (*haifypb.AttachDisklessClientResponse, error) {
	if err := s.resources.AttachDisklessClient(ctx, req.Resource, req.Node); err != nil {
		return &haifypb.AttachDisklessClientResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.AttachDisklessClientResponse{
		Success: true,
		Message: "Diskless client attached successfully",
	}, nil
}

func (s *Server) DetachDisklessClient(ctx context.Context, req *haifypb.DetachDisklessClientRequest) (*haifypb.DetachDisklessClientResponse, error) {
	if err := s.resources.DetachDisklessClient(ctx, req.Resource, req.Node); err != nil {
		return &haifypb.DetachDisklessClientResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.DetachDisklessClientResponse{
		Success: true,
		Message: "Diskless client detached successfully",
	}, nil
}

// SetTiebreaker moves a resource's diskless quorum tiebreaker to another node.
// The previous holder is reported back so the caller can show what changed.
func (s *Server) SetTiebreaker(ctx context.Context, req *haifypb.SetTiebreakerRequest) (*haifypb.SetTiebreakerResponse, error) {
	previous := ""
	if s.ctrl.db != nil {
		if dbRes, err := s.ctrl.db.GetResource(ctx, req.Resource); err == nil && dbRes != nil {
			previous = dbRes.DisklessNodes
		}
	}

	if err := s.resources.SetTiebreaker(ctx, req.Resource, req.Node); err != nil {
		return &haifypb.SetTiebreakerResponse{Success: false, Message: err.Error()}, nil
	}

	msg := fmt.Sprintf("tiebreaker for %q is now %q", req.Resource, req.Node)
	if req.Node == "" {
		// Whether that is dangerous depends on how many diskful replicas are
		// left. Two need the tiebreaker to reach a majority; three already have
		// one without it, and an off-site DR counts — warning regardless would
		// train the operator to ignore the message.
		replicas := 0
		if s.ctrl.db != nil {
			if dbRes, err := s.ctrl.db.GetResource(ctx, req.Resource); err == nil && dbRes != nil {
				replicas = len(splitCSV(dbRes.Nodes))
			}
		}
		if replicas >= 3 {
			msg = fmt.Sprintf("tiebreaker removed from %q; %d replicas still give a quorum majority",
				req.Resource, replicas)
		} else {
			msg = fmt.Sprintf("tiebreaker removed from %q; with %d replicas a single node failure will now suspend I/O",
				req.Resource, replicas)
		}
	}
	return &haifypb.SetTiebreakerResponse{
		Success:      true,
		Message:      msg,
		PreviousNode: previous,
		Node:         req.Node,
	}, nil
}

// AddDR attaches an off-site asynchronous replica to a running resource. The
// port actually used is echoed back, since a zero request port is allocated by
// the controller and the caller needs it to open the firewall.
func (s *Server) AddDR(ctx context.Context, req *haifypb.AddDRRequest) (*haifypb.AddDRResponse, error) {
	if err := s.resources.AddDR(ctx, req.Resource, req.DrNode, req.DrEndpoint, req.WanPort, req.EgressAddress); err != nil {
		return &haifypb.AddDRResponse{Success: false, Message: err.Error()}, nil
	}

	port := req.WanPort
	if s.ctrl.db != nil {
		if dbRes, err := s.ctrl.db.GetResource(ctx, req.Resource); err == nil && dbRes != nil {
			port = uint32(dbRes.WANPort)
		}
	}
	return &haifypb.AddDRResponse{
		Success: true,
		Message: fmt.Sprintf("DR site %q attached to %q; initial sync runs in the background", req.DrNode, req.Resource),
		WanPort: port,
	}, nil
}

// AddReplica adds a diskful local replica to a running resource.
func (s *Server) AddReplica(ctx context.Context, req *haifypb.AddReplicaRequest) (*haifypb.AddReplicaResponse, error) {
	if err := s.resources.AddReplicaWith(ctx, req.Resource, req.Node,
		AddReplicaOpts{IgnoreFreeSpace: req.IgnoreFreeSpace, AllowUnreachable: req.AllowUnreachable}); err != nil {
		return &haifypb.AddReplicaResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.AddReplicaResponse{
		Success: true,
		Message: fmt.Sprintf("replica added on %q; initial sync runs in the background", req.Node),
	}, nil
}

func (s *Server) RemoveReplica(ctx context.Context, req *haifypb.RemoveReplicaRequest) (*haifypb.RemoveReplicaResponse, error) {
	if err := s.resources.RemoveReplicaOptions(ctx, req.Resource, req.Node, req.Lost); err != nil {
		return &haifypb.RemoveReplicaResponse{Success: false, Message: err.Error()}, nil
	}
	if req.Lost {
		return &haifypb.RemoveReplicaResponse{
			Success: true,
			Message: fmt.Sprintf("replica of the lost node %q removed from the survivors; %s",
				req.Node, LostReplicaCleanup(req.Resource, req.Node)),
		}, nil
	}
	return &haifypb.RemoveReplicaResponse{
		Success: true,
		Message: fmt.Sprintf("replica removed from %q", req.Node),
	}, nil
}
