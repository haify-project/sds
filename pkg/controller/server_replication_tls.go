package controller

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

func (s *Server) SetupReplicationTLS(ctx context.Context, req *haifypb.SetupReplicationTLSRequest) (*haifypb.ReplicationTLSResponse, error) {
	states, err := s.resources.SetupReplicationTLS(ctx, req.Nodes)
	if err != nil {
		return &haifypb.ReplicationTLSResponse{Success: false, Message: err.Error()}, nil
	}
	return tlsResponse(states), nil
}

func (s *Server) GetReplicationTLSStatus(ctx context.Context, req *haifypb.GetReplicationTLSStatusRequest) (*haifypb.ReplicationTLSResponse, error) {
	states, err := s.resources.ReplicationTLSStatus(ctx, req.Nodes)
	if err != nil {
		return &haifypb.ReplicationTLSResponse{Success: false, Message: err.Error()}, nil
	}
	return tlsResponse(states), nil
}

func (s *Server) SetResourceTLS(ctx context.Context, req *haifypb.SetResourceTLSRequest) (*haifypb.SetResourceTLSResponse, error) {
	n, err := s.resources.SetResourceTLS(ctx, req.Resource, req.Enabled)
	if err != nil {
		return &haifypb.SetResourceTLSResponse{Success: false, Message: err.Error(), LinksChanged: uint32(n)}, nil
	}
	state := "off"
	if req.Enabled {
		state = "on"
	}
	return &haifypb.SetResourceTLSResponse{
		Success: true, LinksChanged: uint32(n),
		Message: fmt.Sprintf("TLS is %s for every connection of %s (%d link(s) switched)", state, req.Resource, n),
	}, nil
}

func tlsResponse(states []NodeTLSState) *haifypb.ReplicationTLSResponse {
	out := &haifypb.ReplicationTLSResponse{Success: true}
	ready := 0
	for _, st := range states {
		info := &haifypb.NodeTLSInfo{Node: st.Node, Ready: st.Ready, Problem: st.Problem}
		if !st.Expires.IsZero() {
			info.Expires = st.Expires.UTC().Format("2006-01-02")
		}
		if st.Ready {
			ready++
		}
		out.Nodes = append(out.Nodes, info)
	}
	out.Message = fmt.Sprintf("%d of %d node(s) ready for encrypted replication", ready, len(states))
	return out
}
